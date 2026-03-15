package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"
)

// verifyWebhookSignature validates the GitHub webhook HMAC-SHA256 signature.
// Returns the request body on success or an error if verification fails.
func verifyWebhookSignature(r *http.Request, secret string) ([]byte, error) {
	sig := strings.TrimSpace(r.Header.Get("X-Hub-Signature-256"))
	if sig == "" {
		return nil, errors.New("missing X-Hub-Signature-256 header")
	}
	if !strings.HasPrefix(sig, "sha256=") {
		return nil, errors.New("invalid signature format")
	}
	sigHex := strings.TrimPrefix(sig, "sha256=")
	expectedSig, err := hex.DecodeString(sigHex)
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	defer r.Body.Close()

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	computed := mac.Sum(nil)

	if !hmac.Equal(computed, expectedSig) {
		return nil, errors.New("signature mismatch")
	}
	return body, nil
}

// generateAppJWT creates a JWT for GitHub App authentication (RS256).
// The token is valid for 9 minutes with 30 seconds of clock drift tolerance.
func generateAppJWT(appID string, privateKey *rsa.PrivateKey) (string, error) {
	now := time.Now().Unix()
	header := base64URLEncode([]byte(`{"alg":"RS256","typ":"JWT"}`))

	claims, err := json.Marshal(map[string]any{
		"iat": now - 30,
		"exp": now + 540,
		"iss": appID,
	})
	if err != nil {
		return "", err
	}
	payload := base64URLEncode(claims)

	signingInput := header + "." + payload
	hash := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(nil, privateKey, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}

	return signingInput + "." + base64URLEncode(sig), nil
}

// base64URLEncode encodes bytes to base64url without padding.
func base64URLEncode(data []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(data), "=")
}

// parseRSAPrivateKey parses a PEM-encoded RSA private key (PKCS1 or PKCS8).
func parseRSAPrivateKey(pemData []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("PKCS8 key is not RSA")
		}
		return rsaKey, nil
	default:
		return nil, fmt.Errorf("unsupported PEM block type: %s", block.Type)
	}
}

// getInstallationToken exchanges a JWT for an installation access token.
func getInstallationToken(ctx context.Context, installationID int64, jwt string) (string, time.Time, error) {
	url := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", time.Time{}, fmt.Errorf("installation token failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", time.Time{}, fmt.Errorf("decode installation token: %w", err)
	}
	return result.Token, result.ExpiresAt, nil
}

// tokenCache caches an installation access token with expiry tracking.
type tokenCache struct {
	token     string
	expiresAt time.Time
}

func (tc *tokenCache) valid() bool {
	if tc.token == "" {
		return false
	}
	return time.Now().Add(60 * time.Second).Before(tc.expiresAt)
}

// githubClient provides authenticated GitHub API access for a GitHub App.
type githubClient struct {
	appID          string
	installationID int64
	privateKey     *rsa.PrivateKey
	httpClient     *http.Client
	cache          tokenCache
}

func newGitHubClient(appID string, installationID int64, privateKey *rsa.PrivateKey) *githubClient {
	return &githubClient{
		appID:          appID,
		installationID: installationID,
		privateKey:     privateKey,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (gc *githubClient) getToken(ctx context.Context) (string, error) {
	if gc.cache.valid() {
		return gc.cache.token, nil
	}
	jwt, err := generateAppJWT(gc.appID, gc.privateKey)
	if err != nil {
		return "", fmt.Errorf("generate jwt: %w", err)
	}
	token, expiresAt, err := getInstallationToken(ctx, gc.installationID, jwt)
	if err != nil {
		return "", err
	}
	gc.cache = tokenCache{token: token, expiresAt: expiresAt}
	return token, nil
}

func (gc *githubClient) apiRequest(ctx context.Context, method, url string, body io.Reader) (*http.Response, error) {
	token, err := gc.getToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return gc.httpClient.Do(req)
}

// createIssueComment posts a comment on an issue or pull request.
func (gc *githubClient) createIssueComment(ctx context.Context, owner, repo string, number int, body string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d/comments", owner, repo, number)
	payload, _ := json.Marshal(map[string]string{"body": body})
	resp, err := gc.apiRequest(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create comment failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// createPRReview submits a pull request review.
func (gc *githubClient) createPRReview(ctx context.Context, owner, repo string, prNumber int, body string, event string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d/reviews", owner, repo, prNumber)
	payload, _ := json.Marshal(map[string]string{"body": body, "event": event})
	resp, err := gc.apiRequest(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create review failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// getPRDiff fetches the diff for a pull request.
func (gc *githubClient) getPRDiff(ctx context.Context, owner, repo string, prNumber int) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, prNumber)
	token, err := gc.getToken(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.diff")

	resp, err := gc.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get pr diff failed: status=%d", resp.StatusCode)
	}
	return string(data), nil
}

// webhookPayload represents the common fields across GitHub webhook events.
type webhookPayload struct {
	Action       string              `json:"action"`
	Installation webhookInstallation `json:"installation"`
	Repository   webhookRepository   `json:"repository"`
	PullRequest  *webhookPullRequest `json:"pull_request,omitempty"`
	Issue        *webhookIssue       `json:"issue,omitempty"`
	Comment      *webhookComment     `json:"comment,omitempty"`
	Sender       webhookUser         `json:"sender"`
}

type webhookInstallation struct {
	ID int64 `json:"id"`
}

type webhookRepository struct {
	FullName string    `json:"full_name"`
	Owner    repoOwner `json:"owner"`
	Name     string    `json:"name"`
}

type repoOwner struct {
	Login string `json:"login"`
}

type webhookPullRequest struct {
	Number int         `json:"number"`
	Title  string      `json:"title"`
	Body   string      `json:"body"`
	State  string      `json:"state"`
	Head   prRef       `json:"head"`
	Base   prRef       `json:"base"`
	User   webhookUser `json:"user"`
}

type prRef struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type webhookIssue struct {
	Number int         `json:"number"`
	Title  string      `json:"title"`
	Body   string      `json:"body"`
	User   webhookUser `json:"user"`
}

type webhookComment struct {
	Body string      `json:"body"`
	User webhookUser `json:"user"`
}

type webhookUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// parseBigInt parses a decimal string into *big.Int (for strconv-free integer parsing).
func parseBigInt(s string) (int64, error) {
	n := new(big.Int)
	if _, ok := n.SetString(s, 10); !ok {
		return 0, fmt.Errorf("invalid integer: %q", s)
	}
	return n.Int64(), nil
}

// loadPrivateKeyFile reads and parses a PEM-encoded RSA private key from a file path
// or directly from the content if it looks like PEM data.
func loadPrivateKeyFile(pathOrContent string) (*rsa.PrivateKey, error) {
	v := strings.TrimSpace(pathOrContent)
	if v == "" {
		return nil, errors.New("private key path or content is empty")
	}

	var pemData []byte
	if strings.HasPrefix(v, "-----BEGIN") {
		pemData = []byte(v)
	} else {
		var err error
		pemData, err = os.ReadFile(v)
		if err != nil {
			return nil, fmt.Errorf("read private key file: %w", err)
		}
	}
	return parseRSAPrivateKey(pemData)
}
