package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type testRewriteTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t *testRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = t.target.Scheme
	u.Host = t.target.Host
	cloned.URL = &u
	cloned.Host = ""
	return t.base.RoundTrip(cloned)
}

func signWebhookBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestHandleHealth_GETReturnsJSON(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()

	b := &bot{
		cfg: botConfig{botUsername: "qws941_bot"},
		gateway: &gatewayClient{
			baseURL:    gateway.URL,
			httpClient: gateway.Client(),
		},
		pendingJobs: make(map[string]*pendingJob),
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()

	b.handleHealth(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := payload["status"]; got != "ok" {
		t.Fatalf("status field = %v, want ok", got)
	}
	if got, ok := payload["gateway_healthy"].(bool); !ok || !got {
		t.Fatalf("gateway_healthy = %v, want true", payload["gateway_healthy"])
	}
	if got := payload["bot"]; got != "qws941_bot" {
		t.Fatalf("bot field = %v, want qws941_bot", got)
	}
}

func TestHandleWebhook_MethodNotAllowed(t *testing.T) {
	b := &bot{cfg: botConfig{webhookSecret: "secret"}, pendingJobs: make(map[string]*pendingJob)}
	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	rr := httptest.NewRecorder()

	b.handleWebhook(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleWebhook_InvalidSignatureUnauthorized(t *testing.T) {
	body := []byte(`{"action":"opened","sender":{"type":"User"}}`)
	b := &bot{cfg: botConfig{webhookSecret: "secret"}, pendingJobs: make(map[string]*pendingJob)}
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	req.Header.Set("X-GitHub-Event", "pull_request")
	rr := httptest.NewRecorder()

	b.handleWebhook(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHandleWebhook_IgnoresBotSender(t *testing.T) {
	secret := "secret"
	body := []byte(`{"action":"created","sender":{"type":"Bot"},"repository":{"full_name":"o/r","name":"r","owner":{"login":"o"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signWebhookBody(secret, body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-GitHub-Delivery", "d1")

	b := &bot{
		cfg:         botConfig{webhookSecret: secret},
		pendingJobs: make(map[string]*pendingJob),
	}
	rr := httptest.NewRecorder()

	b.handleWebhook(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if strings.TrimSpace(rr.Body.String()) != "ignored: bot sender" {
		t.Fatalf("response body = %q, want ignored message", rr.Body.String())
	}
}

func TestHandleWebhook_DispatchPullRequestEvent(t *testing.T) {
	secret := "secret"
	body := []byte(`{
		"action":"closed",
		"sender":{"type":"User"},
		"repository":{"full_name":"o/r","name":"r","owner":{"login":"o"}},
		"pull_request":{"number":7,"title":"t","body":"b","state":"open","head":{"ref":"h","sha":"1"},"base":{"ref":"m","sha":"2"},"user":{"login":"u","type":"User"}}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signWebhookBody(secret, body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "d-pr")

	b := &bot{
		cfg:         botConfig{webhookSecret: secret},
		pendingJobs: make(map[string]*pendingJob),
	}
	rr := httptest.NewRecorder()

	b.handleWebhook(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := strings.TrimSpace(rr.Body.String()); got != "ok" {
		t.Fatalf("response body = %q, want ok", got)
	}
}

func TestHandleWebhook_DispatchIssueCommentEvent(t *testing.T) {
	secret := "secret"
	var submitted gatewayJobRequest
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jobs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(data, &submitted); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"job_id":"x","status":"queued"}`))
	}))
	defer gateway.Close()

	body := []byte(`{
		"action":"created",
		"sender":{"type":"User"},
		"repository":{"full_name":"o/r","name":"r","owner":{"login":"o"}},
		"issue":{"number":42,"title":"Issue title","body":"desc","user":{"login":"u","type":"User"}},
		"comment":{"body":"@qws941_bot do something","user":{"login":"c","type":"User"}}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signWebhookBody(secret, body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")

	b := &bot{
		cfg: botConfig{
			webhookSecret: secret,
			botUsername:   "qws941_bot",
			callbackBase:  "https://bot.example",
		},
		gateway:     &gatewayClient{baseURL: gateway.URL, httpClient: gateway.Client()},
		pendingJobs: make(map[string]*pendingJob),
	}
	b.github = &githubClient{}

	rr := httptest.NewRecorder()
	b.handleWebhook(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if submitted.JobID != "bot-delivery-1-42" {
		t.Fatalf("job_id = %q, want bot-delivery-1-42", submitted.JobID)
	}
	if submitted.Mode != "async" {
		t.Fatalf("mode = %q, want async", submitted.Mode)
	}
	if submitted.CallbackURL != "https://bot.example/callback" {
		t.Fatalf("callback_url = %q, want https://bot.example/callback", submitted.CallbackURL)
	}
	if !strings.Contains(submitted.Prompt, "User request: do something") {
		t.Fatalf("prompt missing extracted user request: %q", submitted.Prompt)
	}
}

func TestHandleWebhook_IssueCommentMentionAloneUsesDefaultPrompt(t *testing.T) {
	secret := "secret"
	var submitted gatewayJobRequest
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"job_id":"x","status":"queued"}`))
	}))
	defer gateway.Close()

	body := []byte(`{
		"action":"created",
		"sender":{"type":"User"},
		"repository":{"full_name":"o/r","name":"r","owner":{"login":"o"}},
		"issue":{"number":9,"title":"Issue title","body":"desc","user":{"login":"u","type":"User"}},
		"comment":{"body":"@qws941_bot","user":{"login":"c","type":"User"}}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signWebhookBody(secret, body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-GitHub-Delivery", "delivery-2")

	b := &bot{
		cfg: botConfig{
			webhookSecret: secret,
			botUsername:   "qws941_bot",
			callbackBase:  "https://bot.example",
		},
		gateway:     &gatewayClient{baseURL: gateway.URL, httpClient: gateway.Client()},
		pendingJobs: make(map[string]*pendingJob),
	}

	rr := httptest.NewRecorder()
	b.handleWebhook(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if !strings.Contains(submitted.Prompt, "Please review this issue and provide your analysis.") {
		t.Fatalf("default prompt not used: %q", submitted.Prompt)
	}
}

func TestHandleWebhook_DispatchPRReviewCommentEvent(t *testing.T) {
	secret := "secret"
	var submitted gatewayJobRequest
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jobs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"job_id":"x","status":"queued"}`))
	}))
	defer gateway.Close()

	body := []byte(`{
		"action":"created",
		"sender":{"type":"User"},
		"repository":{"full_name":"o/r","name":"r","owner":{"login":"o"}},
		"pull_request":{"number":8,"title":"PR title","body":"desc","state":"open","head":{"ref":"h","sha":"1"},"base":{"ref":"m","sha":"2"},"user":{"login":"u","type":"User"}},
		"comment":{"body":"@qws941_bot review this","user":{"login":"c","type":"User"}}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signWebhookBody(secret, body))
	req.Header.Set("X-GitHub-Event", "pull_request_review_comment")
	req.Header.Set("X-GitHub-Delivery", "delivery-3")

	b := &bot{
		cfg: botConfig{
			webhookSecret: secret,
			botUsername:   "qws941_bot",
			callbackBase:  "https://bot.example",
		},
		gateway:     &gatewayClient{baseURL: gateway.URL, httpClient: gateway.Client()},
		pendingJobs: make(map[string]*pendingJob),
	}

	rr := httptest.NewRecorder()
	b.handleWebhook(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if submitted.JobID != "bot-delivery-3-8" {
		t.Fatalf("job_id = %q, want bot-delivery-3-8", submitted.JobID)
	}
	if !strings.Contains(submitted.Prompt, "User request: review this") {
		t.Fatalf("prompt missing extracted request: %q", submitted.Prompt)
	}
}

func TestHandleCallback_UnknownJobID(t *testing.T) {
	b := &bot{pendingJobs: make(map[string]*pendingJob)}
	cb := gatewayCallbackPayload{JobID: "missing", Status: "completed", Result: "ok"}
	data, _ := json.Marshal(cb)
	req := httptest.NewRequest(http.MethodPost, "/callback", bytes.NewReader(data))
	rr := httptest.NewRecorder()

	b.handleCallback(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestHandleCallback_PRReviewPostsReview(t *testing.T) {
	var reviewCalls int
	var gotBody string
	var gotEvent string

	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/pulls/1/reviews":
			reviewCalls++
			defer r.Body.Close()
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode review payload: %v", err)
			}
			gotBody = payload["body"]
			gotEvent = payload["event"]
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer ghServer.Close()

	targetURL, err := url.Parse(ghServer.URL)
	if err != nil {
		t.Fatalf("parse mock server URL: %v", err)
	}

	gc := &githubClient{
		httpClient: &http.Client{
			Transport: &testRewriteTransport{base: http.DefaultTransport, target: targetURL},
			Timeout:   5 * time.Second,
		},
		cache: tokenCache{token: "cached-token", expiresAt: time.Now().Add(5 * time.Minute)},
	}

	b := &bot{github: gc, pendingJobs: make(map[string]*pendingJob)}
	b.trackJob("job-pr", &pendingJob{Owner: "o", Repo: "r", Number: 1, EventType: "pr_review"})

	cb := gatewayCallbackPayload{JobID: "job-pr", Status: "completed", Result: "LGTM"}
	data, _ := json.Marshal(cb)
	req := httptest.NewRequest(http.MethodPost, "/callback", bytes.NewReader(data))
	rr := httptest.NewRecorder()

	b.handleCallback(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if reviewCalls != 1 {
		t.Fatalf("review calls = %d, want 1", reviewCalls)
	}
	if gotEvent != "COMMENT" {
		t.Fatalf("event = %q, want COMMENT", gotEvent)
	}
	if gotBody != "LGTM" {
		t.Fatalf("body = %q, want LGTM", gotBody)
	}
	if b.popJob("job-pr") != nil {
		t.Fatalf("job should be removed after callback")
	}
}

func TestHandleCallback_IssueCommentStatuses(t *testing.T) {
	type tc struct {
		name          string
		status        string
		result        string
		errMsg        string
		wantSubstring string
	}
	cases := []tc{
		{name: "completed", status: "completed", result: "final analysis", wantSubstring: "final analysis"},
		{name: "completed-empty-fallback", status: "completed", result: "", wantSubstring: "Analysis complete."},
		{name: "failed", status: "failed", errMsg: "boom", wantSubstring: "Analysis failed: boom"},
		{name: "cancelled", status: "cancelled", errMsg: "user requested", wantSubstring: "Analysis cancelled: user requested"},
	}

	for _, c := range cases {
		commentCalls := 0
		gotBody := ""
		ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/repos/o/r/issues/2/comments" {
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
			commentCalls++
			defer r.Body.Close()
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode comment payload: %v", err)
			}
			gotBody = payload["body"]
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		}))

		targetURL, err := url.Parse(ghServer.URL)
		if err != nil {
			t.Fatalf("parse server url: %v", err)
		}

		gc := &githubClient{
			httpClient: &http.Client{
				Transport: &testRewriteTransport{base: http.DefaultTransport, target: targetURL},
				Timeout:   5 * time.Second,
			},
			cache: tokenCache{token: "cached-token", expiresAt: time.Now().Add(5 * time.Minute)},
		}

		b := &bot{github: gc, pendingJobs: make(map[string]*pendingJob)}
		b.trackJob("job-ic", &pendingJob{Owner: "o", Repo: "r", Number: 2, EventType: "issue_comment"})

		cb := gatewayCallbackPayload{
			JobID:  "job-ic",
			Status: c.status,
			Result: c.result,
			Error:  c.errMsg,
		}
		data, _ := json.Marshal(cb)
		req := httptest.NewRequest(http.MethodPost, "/callback", bytes.NewReader(data))
		rr := httptest.NewRecorder()

		b.handleCallback(rr, req)
		ghServer.Close()

		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want %d", c.name, rr.Code, http.StatusOK)
		}
		if commentCalls != 1 {
			t.Fatalf("%s: comment calls = %d, want 1", c.name, commentCalls)
		}
		if !strings.Contains(gotBody, c.wantSubstring) {
			t.Fatalf("%s: body = %q, want substring %q", c.name, gotBody, c.wantSubstring)
		}
	}
}

func TestTrackJobAndPopJob(t *testing.T) {
	b := &bot{pendingJobs: make(map[string]*pendingJob)}
	want := &pendingJob{Owner: "o", Repo: "r", Number: 1, EventType: "issue_comment"}
	b.trackJob("job1", want)

	got := b.popJob("job1")
	if got == nil {
		t.Fatalf("popJob returned nil")
	}
	if *got != *want {
		t.Fatalf("popped job = %+v, want %+v", *got, *want)
	}
	if b.popJob("job1") != nil {
		t.Fatalf("second pop should return nil")
	}
}

func TestExtractPrompt(t *testing.T) {
	if got := extractPrompt("@bot do something", "@bot"); got != "do something" {
		t.Fatalf("extractPrompt mention + text = %q, want %q", got, "do something")
	}
	if got := extractPrompt("@bot", "@bot"); got != "" {
		t.Fatalf("extractPrompt mention only = %q, want empty string", got)
	}
	body := "plain comment without mention"
	if got := extractPrompt(body, "@bot"); got != body {
		t.Fatalf("extractPrompt without mention = %q, want %q", got, body)
	}
}

func TestHandleCallback_MethodNotAllowed(t *testing.T) {
	b := &bot{pendingJobs: make(map[string]*pendingJob)}
	req := httptest.NewRequest(http.MethodGet, "/callback", nil)
	rr := httptest.NewRecorder()

	b.handleCallback(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleCallback_BadPayload(t *testing.T) {
	b := &bot{pendingJobs: make(map[string]*pendingJob)}
	req := httptest.NewRequest(http.MethodPost, "/callback", strings.NewReader("{"))
	rr := httptest.NewRecorder()

	b.handleCallback(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleWebhook_ValidSignatureBodyRoundTrip(t *testing.T) {
	secret := "secret"
	body := []byte(`{"action":"created","sender":{"type":"User"},"repository":{"full_name":"o/r","name":"r","owner":{"login":"o"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signWebhookBody(secret, body))
	req.Header.Set("X-GitHub-Event", "unknown_event")
	req.Header.Set("X-GitHub-Delivery", "d-x")

	b := &bot{cfg: botConfig{webhookSecret: secret}, pendingJobs: make(map[string]*pendingJob)}
	rr := httptest.NewRecorder()

	b.handleWebhook(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestHandleWebhook_PRReviewCommentWithoutMentionNoSubmit(t *testing.T) {
	secret := "secret"
	calls := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job_id":"x","status":"queued"}`))
	}))
	defer gateway.Close()

	body := []byte(`{
		"action":"created",
		"sender":{"type":"User"},
		"repository":{"full_name":"o/r","name":"r","owner":{"login":"o"}},
		"pull_request":{"number":3,"title":"PR","body":"desc","state":"open","head":{"ref":"h","sha":"1"},"base":{"ref":"m","sha":"2"},"user":{"login":"u","type":"User"}},
		"comment":{"body":"no bot mention","user":{"login":"c","type":"User"}}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signWebhookBody(secret, body))
	req.Header.Set("X-GitHub-Event", "pull_request_review_comment")
	req.Header.Set("X-GitHub-Delivery", "d-no-mention")

	b := &bot{
		cfg:         botConfig{webhookSecret: secret, botUsername: "qws941_bot", callbackBase: "https://bot.example"},
		gateway:     &gatewayClient{baseURL: gateway.URL, httpClient: gateway.Client()},
		pendingJobs: make(map[string]*pendingJob),
	}

	rr := httptest.NewRecorder()
	b.handleWebhook(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if calls != 0 {
		t.Fatalf("gateway calls = %d, want 0", calls)
	}
}
