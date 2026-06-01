// Package mcpsrv wires the sweep-mcp tools into an MCP server.
//
// Same flat-list pattern as tor-character-mcp-docker. Each entry is
// (tool name, register fn). For v1 just one tool — run_sweep — which
// kicks off the n8n model-sweep-kickoff webhook and returns the
// tracking issue URL + sweep id.
package mcpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dvystrcil/sweep-mcp-docker/internal/sweep"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterAll attaches every sweep-mcp tool to the server.
func RegisterAll(server *mcp.Server, client *sweep.Client) []string {
	names := []string{}
	for _, t := range []struct {
		name string
		add  func(*mcp.Server, *sweep.Client)
	}{
		{"run_sweep", addRunSweep},
	} {
		t.add(server, client)
		names = append(names, t.name)
	}
	return names
}

// ---- run_sweep ----

type runSweepArgs struct {
	Model     string `json:"model"`
	Payload   string `json:"payload,omitempty"`
	Runs      string `json:"runs,omitempty"`
	Requester string `json:"requester,omitempty"`
}

func addRunSweep(server *mcp.Server, client *sweep.Client) {
	tool := &mcp.Tool{
		Name: "run_sweep",
		Description: "Trigger a model-testing sweep against the homelab ARC runner. " +
			"Forwards to the n8n model-sweep-kickoff webhook, which dispatches the " +
			"GHA workflow and files a tracking GitHub issue. Returns immediately " +
			"with sweep_id + issue_url; the sweep itself runs for 5-90 minutes " +
			"depending on the payload set. The tracking issue gets dual-summarize " +
			"comments (local LLM + Claude) when the sweep completes. " +
			"Single-sweep mutex enforced server-side: a second call while a " +
			"sweep is in flight returns an error pointing at the in-flight run.",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"model": {
					Type: "string",
					Description: "The ollama model tag to sweep (e.g. 'qwen3.6:35b', " +
						"'devstral:24b'). Must exist on the cluster's ollama service.",
				},
				"payload": {
					Type: "string",
					Description: "Which benchmark payload(s) to run. 'all' (default) " +
						"runs every payload (~1-2 hours); a single name like " +
						"'instruction_following' runs just that one (~5-15 min). " +
						"Names match files in benchmarks/payloads/ in the " +
						"model-testing repo.",
				},
				"runs": {
					Type: "string",
					Description: "Number of runs per (model, payload) pair. Default '1'. " +
						"Higher values average out noise but multiply runtime.",
				},
				"requester": {
					Type: "string",
					Description: "Free-form identifier for who/what asked. " +
						"Surfaces in the tracking issue title. Default 'mcp:unknown'. " +
						"LLMs should fill this in with something distinguishable, " +
						"e.g. 'mcp:chat-session-2026-06-01'.",
				},
			},
			Required: []string{"model"},
		},
	}
	server.AddTool(tool, handleRunSweep(client))
}

func handleRunSweep(client *sweep.Client) func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args runSweepArgs
		if errResult := decodeArgs(req, &args); errResult != nil {
			return errResult, nil
		}
		if args.Model == "" {
			return errorResult("required field 'model' is missing or empty"), nil
		}
		kickoff := sweep.KickoffRequest{
			Model:     args.Model,
			Payload:   args.Payload,
			Runs:      args.Runs,
			Requester: requesterOrDefault(args.Requester),
		}
		resp, err := client.Kickoff(ctx, kickoff)
		if err != nil {
			var conflictErr *sweep.ConflictError
			if errors.As(err, &conflictErr) {
				return errorResult(conflictErr.Error()), nil
			}
			return errorResult(fmt.Sprintf("kickoff failed: %v", err)), nil
		}
		return jsonResult(resp), nil
	}
}

func decodeArgs(req *mcp.CallToolRequest, dst any) *mcp.CallToolResult {
	if len(req.Params.Arguments) == 0 {
		return nil
	}
	if err := json.Unmarshal(req.Params.Arguments, dst); err != nil {
		return errorResult(fmt.Sprintf("decode arguments: %v", err))
	}
	return nil
}

func requesterOrDefault(s string) string {
	if s == "" {
		return "mcp:unknown"
	}
	return s
}

// ---- helpers (mirror tor-character-mcp pattern) ----

func jsonResult(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errorResult(fmt.Sprintf("marshal result: %v", err))
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
	}
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}
