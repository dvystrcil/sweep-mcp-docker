// Package sweep is the HTTP client to the n8n model-sweep-kickoff
// webhook. Single responsibility: POST a JSON body, decode the
// response, surface clear errors to the MCP layer.
package sweep

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

// Client is a thin HTTP wrapper around the n8n model-sweep-kickoff
// webhook. Safe for concurrent use — the underlying http.Client is.
type Client struct {
	kickoffURL string
	httpc      *http.Client
}

// NewClient constructs a Client. timeout caps each request.
func NewClient(kickoffURL string, timeout time.Duration) *Client {
	return &Client{
		kickoffURL: kickoffURL,
		httpc:      &http.Client{Timeout: timeout},
	}
}

// KickoffRequest mirrors the n8n workflow's webhook body shape.
//
// `Model` is the only required field. The other three carry the
// defaults the n8n side already applies if empty.
type KickoffRequest struct {
	Model     string `json:"model"`
	Payload   string `json:"payload,omitempty"`
	Runs      string `json:"runs,omitempty"`
	Requester string `json:"requester,omitempty"`
}

// KickoffResponse mirrors the n8n workflow's 200 OK body shape.
//
// On 409 (single-sweep mutex tripped), the body shape is different —
// we surface the conflict as an error with the in-flight URL.
type KickoffResponse struct {
	Status       string `json:"status"`
	SweepID      any    `json:"sweep_id"` // n8n returns an int; allow both
	RunURL       string `json:"run_url"`
	IssueURL     string `json:"issue_url"`
	IssueNumber  int    `json:"issue_number"`
	Model        string `json:"model"`
	Requester    string `json:"requester"`
	ETA          string `json:"eta"`
}

// ConflictResponse is the n8n 409 body when a sweep is already in flight.
type ConflictResponse struct {
	Error          string                 `json:"error"`
	Reason         string                 `json:"reason"`
	RequestedModel string                 `json:"requested_model"`
	InFlight       map[string]interface{} `json:"in_flight"`
	Guidance       string                 `json:"guidance"`
}

// Kickoff POSTs to the n8n webhook. Returns the parsed response on
// 200, or a structured error on non-2xx. Specifically: a 409 yields a
// ConflictError with the in-flight run details, so the MCP layer can
// surface "wait for the running sweep" cleanly.
func (c *Client) Kickoff(ctx context.Context, req KickoffRequest) (*KickoffResponse, error) {
	if strings.TrimSpace(req.Model) == "" {
		return nil, fmt.Errorf("model is required")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.kickoffURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", c.kickoffURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var out KickoffResponse
		if err := json.Unmarshal(respBody, &out); err != nil {
			return nil, fmt.Errorf("decode 200 body (%q): %w", truncate(string(respBody), 200), err)
		}
		return &out, nil
	case http.StatusConflict:
		var cr ConflictResponse
		if err := json.Unmarshal(respBody, &cr); err != nil {
			return nil, fmt.Errorf("decode 409 body (%q): %w", truncate(string(respBody), 200), err)
		}
		return nil, &ConflictError{Response: cr}
	default:
		return nil, fmt.Errorf("n8n returned HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
}

// ConflictError signals the single-sweep mutex tripped. The MCP layer
// surfaces this with the in-flight run URL so the operator (or LLM)
// knows to wait.
type ConflictError struct {
	Response ConflictResponse
}

func (e *ConflictError) Error() string {
	if e.Response.InFlight != nil {
		url, _ := e.Response.InFlight["html_url"].(string)
		return fmt.Sprintf("sweep already in flight: %s — %s", url, e.Response.Guidance)
	}
	return e.Response.Error
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
