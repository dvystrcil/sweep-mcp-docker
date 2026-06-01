package sweep

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestKickoff_Success(t *testing.T) {
	t.Parallel()

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"status": "dispatched",
			"sweep_id": 26773325191,
			"run_url": "https://github.com/dvystrcil/model-testing/actions/runs/26773325191",
			"issue_url": "https://github.com/dvystrcil/model-testing/issues/40",
			"issue_number": 40,
			"model": "devstral:24b",
			"requester": "n8n:mcp-smoke",
			"eta": "1-2 hours"
		}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 5*time.Second)
	resp, err := c.Kickoff(context.Background(), KickoffRequest{
		Model:     "devstral:24b",
		Payload:   "instruction_following",
		Runs:      "1",
		Requester: "mcp-smoke",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got["model"] != "devstral:24b" {
		t.Errorf("expected model 'devstral:24b' in request body, got %v", got["model"])
	}
	if got["requester"] != "mcp-smoke" {
		t.Errorf("expected requester 'mcp-smoke', got %v", got["requester"])
	}
	if resp.IssueNumber != 40 {
		t.Errorf("expected issue_number 40, got %d", resp.IssueNumber)
	}
	if resp.IssueURL != "https://github.com/dvystrcil/model-testing/issues/40" {
		t.Errorf("unexpected issue_url: %s", resp.IssueURL)
	}
}

func TestKickoff_Conflict(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{
			"error": "sweep already in-flight; single-sweep mutex prevents parallel runs",
			"reason": "gfx1151 ollama wedge prevention",
			"requested_model": "qwen3.6:35b",
			"in_flight": {
				"run_id": 99999999999,
				"started_at": "2026-06-01T17:00:00Z",
				"html_url": "https://github.com/dvystrcil/model-testing/actions/runs/99999999999"
			},
			"guidance": "Wait for the in-flight run to complete."
		}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 5*time.Second)
	_, err := c.Kickoff(context.Background(), KickoffRequest{Model: "qwen3.6:35b"})
	if err == nil {
		t.Fatal("expected error for 409 conflict, got nil")
	}

	var conflictErr *ConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("expected *ConflictError, got %T: %v", err, err)
	}
	if !strings.Contains(conflictErr.Error(), "99999999999") {
		t.Errorf("expected error message to include in-flight URL, got: %s", conflictErr.Error())
	}
	if !strings.Contains(conflictErr.Error(), "Wait for the in-flight run") {
		t.Errorf("expected error message to include guidance, got: %s", conflictErr.Error())
	}
}

func TestKickoff_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`<html>n8n down</html>`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 5*time.Second)
	_, err := c.Kickoff(context.Background(), KickoffRequest{Model: "devstral:24b"})
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to mention 500, got: %v", err)
	}
}

func TestKickoff_MissingModel(t *testing.T) {
	t.Parallel()

	// no httptest server needed — the client should reject before
	// the HTTP call when Model is empty
	c := NewClient("http://unreachable.invalid", 5*time.Second)
	_, err := c.Kickoff(context.Background(), KickoffRequest{Model: ""})
	if err == nil {
		t.Fatal("expected error for empty model, got nil")
	}
	if !strings.Contains(err.Error(), "model is required") {
		t.Errorf("expected 'model is required' in error, got: %v", err)
	}
}

func TestKickoff_NetworkError(t *testing.T) {
	t.Parallel()

	c := NewClient("http://127.0.0.1:1/unreachable", 1*time.Second)
	_, err := c.Kickoff(context.Background(), KickoffRequest{Model: "devstral:24b"})
	if err == nil {
		t.Fatal("expected error for unreachable URL, got nil")
	}
}

func TestConflictError_Format(t *testing.T) {
	t.Parallel()

	ce := &ConflictError{
		Response: ConflictResponse{
			Error: "fallback error",
			InFlight: map[string]interface{}{
				"html_url": "https://example.com/runs/123",
			},
			Guidance: "wait it out",
		},
	}
	got := ce.Error()
	if !strings.Contains(got, "https://example.com/runs/123") {
		t.Errorf("expected URL in message, got: %s", got)
	}
	if !strings.Contains(got, "wait it out") {
		t.Errorf("expected guidance in message, got: %s", got)
	}

	// Missing InFlight should fall back to Response.Error
	ce2 := &ConflictError{Response: ConflictResponse{Error: "plain error"}}
	if ce2.Error() != "plain error" {
		t.Errorf("expected fallback to Response.Error, got: %s", ce2.Error())
	}
}
