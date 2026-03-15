package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// gatewayClient communicates with the opencode-agent-gateway.
type gatewayClient struct {
	baseURL    string
	httpClient *http.Client
}

func newGatewayClient(baseURL string) *gatewayClient {
	return &gatewayClient{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// gatewayJobRequest is the payload for POST /jobs.
type gatewayJobRequest struct {
	JobID       string `json:"job_id"`
	Prompt      string `json:"prompt"`
	Repo        string `json:"repo"`
	Model       string `json:"model,omitempty"`
	Mode        string `json:"mode"`
	CallbackURL string `json:"callback_url"`
}

// gatewayJobResponse is the response from POST /jobs.
type gatewayJobResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// gatewayCallbackPayload is the callback payload from the gateway.
type gatewayCallbackPayload struct {
	JobID       string `json:"job_id"`
	Status      string `json:"status"`
	SessionID   string `json:"session_id,omitempty"`
	Result      string `json:"result,omitempty"`
	Error       string `json:"error,omitempty"`
	DurationMs  int64  `json:"duration_ms"`
	CompletedAt string `json:"completed_at"`
}

// submitJob sends a job to the gateway and returns the response.
func (gc *gatewayClient) submitJob(ctx context.Context, req gatewayJobRequest) (*gatewayJobResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal job request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, gc.baseURL+"/jobs", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := gc.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("submit job: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("submit job failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var result gatewayJobResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode job response: %w", err)
	}
	return &result, nil
}

// checkHealth verifies the gateway is reachable.
func (gc *gatewayClient) checkHealth(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gc.baseURL+"/health", nil)
	if err != nil {
		return false, err
	}
	resp, err := gc.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}
