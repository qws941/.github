package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubmitJob(t *testing.T) {
	var gotReq gatewayJobRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/jobs" {
			t.Fatalf("path = %s, want /jobs", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type = %q, want application/json", got)
		}
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotReq); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"job_id":"job-1","status":"queued"}`))
	}))
	defer server.Close()

	gc := &gatewayClient{baseURL: server.URL, httpClient: server.Client()}
	in := gatewayJobRequest{
		JobID:       "job-1",
		Prompt:      "review this",
		Repo:        "o/r",
		Mode:        "async",
		CallbackURL: "https://bot.example/callback",
	}
	out, err := gc.submitJob(context.Background(), in)
	if err != nil {
		t.Fatalf("submitJob error: %v", err)
	}
	if out.JobID != "job-1" || out.Status != "queued" {
		t.Fatalf("response = %+v, want job_id=job-1 status=queued", out)
	}
	if gotReq != in {
		t.Fatalf("submitted request = %+v, want %+v", gotReq, in)
	}
}

func TestSubmitJobHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("gateway unavailable"))
	}))
	defer server.Close()

	gc := &gatewayClient{baseURL: server.URL, httpClient: server.Client()}
	_, err := gc.submitJob(context.Background(), gatewayJobRequest{JobID: "j", Prompt: "p", Repo: "o/r", Mode: "async", CallbackURL: "https://cb"})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "status=502") || !strings.Contains(msg, "gateway unavailable") {
		t.Fatalf("error = %q, expected status and body details", msg)
	}
}

func TestSubmitJobDecodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()

	gc := &gatewayClient{baseURL: server.URL, httpClient: server.Client()}
	_, err := gc.submitJob(context.Background(), gatewayJobRequest{JobID: "j", Prompt: "p", Repo: "o/r", Mode: "async", CallbackURL: "https://cb"})
	if err == nil {
		t.Fatalf("expected decode error, got nil")
	}
	if !strings.Contains(err.Error(), "decode job response") {
		t.Fatalf("error = %q, want decode job response", err.Error())
	}
}

func TestCheckHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/health" {
			t.Fatalf("path = %s, want /health", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	gc := &gatewayClient{baseURL: server.URL, httpClient: server.Client()}
	ok, err := gc.checkHealth(context.Background())
	if err != nil {
		t.Fatalf("checkHealth error: %v", err)
	}
	if !ok {
		t.Fatalf("checkHealth = false, want true")
	}
}

func TestCheckHealthNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	gc := &gatewayClient{baseURL: server.URL, httpClient: server.Client()}
	ok, err := gc.checkHealth(context.Background())
	if err != nil {
		t.Fatalf("checkHealth error: %v", err)
	}
	if ok {
		t.Fatalf("checkHealth = true, want false")
	}
}
