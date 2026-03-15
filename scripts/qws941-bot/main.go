package main

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// botConfig holds all configuration for the bot server.
type botConfig struct {
	listenAddr     string
	webhookSecret  string
	appID          string
	installationID int64
	privateKey     *rsa.PrivateKey
	gatewayURL     string
	callbackBase   string
	botUsername    string
}

// bot is the main server struct.
type bot struct {
	cfg     botConfig
	github  *githubClient
	gateway *gatewayClient

	// pendingJobs tracks gateway job_id → event context for callback handling.
	mu          sync.Mutex
	pendingJobs map[string]*pendingJob
}

type pendingJob struct {
	Owner     string
	Repo      string
	Number    int
	EventType string // "pr_review", "issue_comment", "pr_comment"
}

func newBot(cfg botConfig) *bot {
	return &bot{
		cfg:         cfg,
		github:      newGitHubClient(cfg.appID, cfg.installationID, cfg.privateKey),
		gateway:     newGatewayClient(cfg.gatewayURL),
		pendingJobs: make(map[string]*pendingJob),
	}
}

// handleHealth returns bot health status.
func (b *bot) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	gatewayOK, _ := b.gateway.checkHealth(r.Context())
	writeJSONResponse(w, map[string]any{
		"status":          "ok",
		"gateway_healthy": gatewayOK,
		"bot":             b.cfg.botUsername,
	})
}

// handleWebhook processes incoming GitHub webhook events.
func (b *bot) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	body, err := verifyWebhookSignature(r, b.cfg.webhookSecret)
	if err != nil {
		fmt.Fprintf(os.Stderr, "webhook signature verification failed: %v\n", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	eventType := r.Header.Get("X-GitHub-Event")
	deliveryID := r.Header.Get("X-GitHub-Delivery")
	fmt.Fprintf(os.Stderr, "webhook received: event=%s delivery=%s\n", eventType, deliveryID)

	var payload webhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		fmt.Fprintf(os.Stderr, "webhook parse failed: %v\n", err)
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	// Ignore events from bots (including ourselves)
	if payload.Sender.Type == "Bot" {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ignored: bot sender")
		return
	}

	switch eventType {
	case "pull_request":
		b.handlePullRequestEvent(r.Context(), payload, deliveryID)
	case "issue_comment":
		b.handleIssueCommentEvent(r.Context(), payload, deliveryID)
	case "pull_request_review_comment":
		b.handlePRReviewCommentEvent(r.Context(), payload, deliveryID)
	default:
		fmt.Fprintf(os.Stderr, "unhandled event type: %s\n", eventType)
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

// handlePullRequestEvent processes pull_request webhook events.
func (b *bot) handlePullRequestEvent(ctx context.Context, payload webhookPayload, deliveryID string) {
	if payload.PullRequest == nil {
		return
	}

	switch payload.Action {
	case "opened", "synchronize":
		// Auto-review on new PR or new commits pushed
		b.submitPRReview(ctx, payload, deliveryID)
	default:
		fmt.Fprintf(os.Stderr, "pr event ignored: action=%s\n", payload.Action)
	}
}

// handleIssueCommentEvent processes issue_comment webhook events.
// Triggers when someone mentions @qws941_bot in an issue or PR comment.
func (b *bot) handleIssueCommentEvent(ctx context.Context, payload webhookPayload, deliveryID string) {
	if payload.Action != "created" {
		return
	}
	if payload.Comment == nil {
		return
	}

	mention := "@" + b.cfg.botUsername
	if !strings.Contains(payload.Comment.Body, mention) {
		return
	}

	// Extract the prompt (everything after the mention)
	prompt := extractPrompt(payload.Comment.Body, mention)
	if strings.TrimSpace(prompt) == "" {
		prompt = "Please review this issue and provide your analysis."
	}

	owner := payload.Repository.Owner.Login
	repo := payload.Repository.Name
	number := 0
	if payload.Issue != nil {
		number = payload.Issue.Number
	}

	jobID := fmt.Sprintf("bot-%s-%d", deliveryID, number)
	b.trackJob(jobID, &pendingJob{
		Owner:     owner,
		Repo:      repo,
		Number:    number,
		EventType: "issue_comment",
	})

	fullPrompt := fmt.Sprintf("Repository: %s/%s\nIssue #%d: %s\n\nUser request: %s",
		owner, repo, number, payload.Issue.Title, prompt)

	_, err := b.gateway.submitJob(ctx, gatewayJobRequest{
		JobID:       jobID,
		Prompt:      fullPrompt,
		Repo:        payload.Repository.FullName,
		Mode:        "run",
		CallbackURL: b.cfg.callbackBase + "/callback",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gateway submit failed: job_id=%s err=%v\n", jobID, err)
		return
	}
	fmt.Fprintf(os.Stderr, "job submitted for issue comment: job_id=%s repo=%s issue=#%d\n", jobID, payload.Repository.FullName, number)
}

// handlePRReviewCommentEvent processes pull_request_review_comment events.
func (b *bot) handlePRReviewCommentEvent(ctx context.Context, payload webhookPayload, deliveryID string) {
	if payload.Action != "created" {
		return
	}
	if payload.Comment == nil {
		return
	}

	mention := "@" + b.cfg.botUsername
	if !strings.Contains(payload.Comment.Body, mention) {
		return
	}

	prompt := extractPrompt(payload.Comment.Body, mention)
	if strings.TrimSpace(prompt) == "" {
		prompt = "Please review this code and provide feedback."
	}

	owner := payload.Repository.Owner.Login
	repo := payload.Repository.Name
	number := 0
	if payload.PullRequest != nil {
		number = payload.PullRequest.Number
	}

	jobID := fmt.Sprintf("bot-%s-%d", deliveryID, number)
	b.trackJob(jobID, &pendingJob{
		Owner:     owner,
		Repo:      repo,
		Number:    number,
		EventType: "pr_comment",
	})

	fullPrompt := fmt.Sprintf("Repository: %s/%s\nPR #%d\n\nUser request: %s", owner, repo, number, prompt)

	_, err := b.gateway.submitJob(ctx, gatewayJobRequest{
		JobID:       jobID,
		Prompt:      fullPrompt,
		Repo:        payload.Repository.FullName,
		Mode:        "run",
		CallbackURL: b.cfg.callbackBase + "/callback",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gateway submit failed: job_id=%s err=%v\n", jobID, err)
		return
	}
	fmt.Fprintf(os.Stderr, "job submitted for pr comment: job_id=%s repo=%s pr=#%d\n", jobID, payload.Repository.FullName, number)
}

// submitPRReview submits a PR review job to the gateway.
func (b *bot) submitPRReview(ctx context.Context, payload webhookPayload, deliveryID string) {
	pr := payload.PullRequest
	owner := payload.Repository.Owner.Login
	repo := payload.Repository.Name

	// Fetch the diff
	diff, err := b.github.getPRDiff(ctx, owner, repo, pr.Number)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get pr diff: %v\n", err)
		diff = "(diff unavailable)"
	}

	// Truncate diff if too large
	const maxDiffLen = 50000
	if len(diff) > maxDiffLen {
		diff = diff[:maxDiffLen] + "\n\n... (truncated)"
	}

	prompt := fmt.Sprintf(`Review this pull request.

Repository: %s/%s
PR #%d: %s
Branch: %s → %s
Author: %s

Description:
%s

Diff:
%s

Provide a concise code review focusing on:
1. Bugs or logic errors
2. Security concerns
3. Performance issues
4. Code style and best practices

Format your response as a GitHub PR review comment (markdown).`,
		owner, repo, pr.Number, pr.Title,
		pr.Head.Ref, pr.Base.Ref, pr.User.Login,
		pr.Body, diff)

	jobID := fmt.Sprintf("bot-%s-pr%d", deliveryID, pr.Number)
	b.trackJob(jobID, &pendingJob{
		Owner:     owner,
		Repo:      repo,
		Number:    pr.Number,
		EventType: "pr_review",
	})

	_, err = b.gateway.submitJob(ctx, gatewayJobRequest{
		JobID:       jobID,
		Prompt:      prompt,
		Repo:        payload.Repository.FullName,
		Mode:        "run",
		CallbackURL: b.cfg.callbackBase + "/callback",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gateway submit failed: job_id=%s err=%v\n", jobID, err)
		return
	}
	fmt.Fprintf(os.Stderr, "pr review job submitted: job_id=%s repo=%s pr=#%d\n", jobID, payload.Repository.FullName, pr.Number)
}

// handleCallback processes gateway callback responses and posts results back to GitHub.
func (b *bot) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var cb gatewayCallbackPayload
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&cb); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	fmt.Fprintf(os.Stderr, "callback received: job_id=%s status=%s duration_ms=%d\n", cb.JobID, cb.Status, cb.DurationMs)

	pending := b.popJob(cb.JobID)
	if pending == nil {
		fmt.Fprintf(os.Stderr, "callback for unknown job: job_id=%s\n", cb.JobID)
		w.WriteHeader(http.StatusOK)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var responseBody string
	switch cb.Status {
	case "completed":
		responseBody = cb.Result
		if responseBody == "" || responseBody == "async session completed" {
			responseBody = "Analysis complete. (No detailed output available — check the OpenCode session for full results.)"
		}
	case "failed":
		responseBody = fmt.Sprintf("❌ Analysis failed: %s", cb.Error)
	case "cancelled":
		responseBody = fmt.Sprintf("⚠️ Analysis cancelled: %s", cb.Error)
	default:
		responseBody = fmt.Sprintf("Job finished with status: %s", cb.Status)
	}

	switch pending.EventType {
	case "pr_review":
		event := "COMMENT"
		if err := b.github.createPRReview(ctx, pending.Owner, pending.Repo, pending.Number, responseBody, event); err != nil {
			fmt.Fprintf(os.Stderr, "failed to create pr review: job_id=%s err=%v\n", cb.JobID, err)
			// Fallback to issue comment
			if err2 := b.github.createIssueComment(ctx, pending.Owner, pending.Repo, pending.Number, responseBody); err2 != nil {
				fmt.Fprintf(os.Stderr, "fallback comment also failed: job_id=%s err=%v\n", cb.JobID, err2)
			}
		} else {
			fmt.Fprintf(os.Stderr, "pr review posted: job_id=%s repo=%s/%s pr=#%d\n", cb.JobID, pending.Owner, pending.Repo, pending.Number)
		}
	case "issue_comment", "pr_comment":
		if err := b.github.createIssueComment(ctx, pending.Owner, pending.Repo, pending.Number, responseBody); err != nil {
			fmt.Fprintf(os.Stderr, "failed to create comment: job_id=%s err=%v\n", cb.JobID, err)
		} else {
			fmt.Fprintf(os.Stderr, "comment posted: job_id=%s repo=%s/%s issue=#%d\n", cb.JobID, pending.Owner, pending.Repo, pending.Number)
		}
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func (b *bot) trackJob(jobID string, pj *pendingJob) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pendingJobs[jobID] = pj
}

func (b *bot) popJob(jobID string) *pendingJob {
	b.mu.Lock()
	defer b.mu.Unlock()
	pj, ok := b.pendingJobs[jobID]
	if !ok {
		return nil
	}
	delete(b.pendingJobs, jobID)
	return pj
}

// extractPrompt extracts the user's message after a @mention.
func extractPrompt(body, mention string) string {
	idx := strings.Index(body, mention)
	if idx < 0 {
		return body
	}
	return strings.TrimSpace(body[idx+len(mention):])
}

func writeJSONResponse(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func envOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v != "" {
		return v
	}
	return fallback
}

func main() {
	listenAddr := envOrDefault("BOT_LISTEN", "0.0.0.0:1111")
	webhookSecret := envOrDefault("BOT_WEBHOOK_SECRET", "")
	appID := envOrDefault("BOT_APP_ID", "")
	installationIDStr := envOrDefault("BOT_INSTALLATION_ID", "")
	privateKeyPath := envOrDefault("BOT_PRIVATE_KEY_PATH", "")
	gatewayURL := envOrDefault("BOT_GATEWAY_URL", "http://127.0.0.1:7800")
	callbackBase := envOrDefault("BOT_CALLBACK_BASE", "")
	botUsername := envOrDefault("BOT_USERNAME", "qws941_bot")

	if webhookSecret == "" {
		fmt.Fprintln(os.Stderr, "fatal: BOT_WEBHOOK_SECRET is required")
		os.Exit(1)
	}
	if appID == "" {
		fmt.Fprintln(os.Stderr, "fatal: BOT_APP_ID is required")
		os.Exit(1)
	}
	if installationIDStr == "" {
		fmt.Fprintln(os.Stderr, "fatal: BOT_INSTALLATION_ID is required")
		os.Exit(1)
	}
	if privateKeyPath == "" {
		fmt.Fprintln(os.Stderr, "fatal: BOT_PRIVATE_KEY_PATH is required")
		os.Exit(1)
	}
	if callbackBase == "" {
		fmt.Fprintln(os.Stderr, "fatal: BOT_CALLBACK_BASE is required (e.g. https://bot.jclee.me)")
		os.Exit(1)
	}

	installationID, err := parseBigInt(installationIDStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: invalid BOT_INSTALLATION_ID: %v\n", err)
		os.Exit(1)
	}

	privateKey, err := loadPrivateKeyFile(privateKeyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: load private key: %v\n", err)
		os.Exit(1)
	}

	cfg := botConfig{
		listenAddr:     listenAddr,
		webhookSecret:  webhookSecret,
		appID:          appID,
		installationID: installationID,
		privateKey:     privateKey,
		gatewayURL:     gatewayURL,
		callbackBase:   strings.TrimRight(callbackBase, "/"),
		botUsername:    botUsername,
	}

	b := newBot(cfg)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", b.handleHealth)
	mux.HandleFunc("/webhook", b.handleWebhook)
	mux.HandleFunc("/callback", b.handleCallback)

	server := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		fmt.Fprintf(os.Stderr, "qws941_bot listening on %s gateway=%s bot=%s\n", cfg.listenAddr, cfg.gatewayURL, cfg.botUsername)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	fmt.Fprintln(os.Stderr, "shutdown signal received")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown error: %v\n", err)
	}
	fmt.Fprintln(os.Stderr, "shutdown complete")
}
