// sweep-mcp exposes model-testing sweep automation as MCP tools.
//
// Composes Phase 3 of model-testing#36: the operator says "Run
// qwen3.7:35b through the sweep test" in OWUI chat; the LLM dispatches
// run_sweep; this server forwards to the n8n kickoff webhook; the
// existing Phase 1 + 2 chain takes over (GHA sweep → dual-summarize →
// tracking issue + Discord ping).
//
// Configuration:
//
//	--http=:8080
//	    bind address for the Streamable HTTP MCP endpoint and /healthz
//
//	--n8n-kickoff-url=<url>
//	    n8n kickoff webhook URL. Defaults to the in-cluster service URL.
//	    Override via env N8N_KICKOFF_URL (the flag takes precedence).
//
// Tracked: dvystrcil/model-testing#36 Phase 3.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dvystrcil/sweep-mcp-docker/internal/mcpsrv"
	"github.com/dvystrcil/sweep-mcp-docker/internal/sweep"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverName    = "sweep-mcp"
	serverVersion = "v0.1.0"

	defaultN8nKickoffURL = "http://n8n.n8n-workflow.svc.cluster.local/webhook/model-sweep-kickoff"
)

var version = "dev" // set via -ldflags="-X main.version=..." in CI

func main() {
	httpAddr := flag.String("http", ":8080", "bind address for HTTP MCP endpoint (e.g. :8080)")
	n8nKickoffURL := flag.String(
		"n8n-kickoff-url",
		envOr("N8N_KICKOFF_URL", defaultN8nKickoffURL),
		"n8n model-sweep-kickoff webhook URL (env: N8N_KICKOFF_URL)",
	)
	flag.Parse()

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, nil)

	client := sweep.NewClient(*n8nKickoffURL, 30*time.Second)
	registered := mcpsrv.RegisterAll(srv, client)
	log.Printf("Starting %s %s (build: %s)", serverName, serverVersion, version)
	log.Printf("Forwarding sweeps to %s", *n8nKickoffURL)
	log.Printf("Registered %d tool(s): %s", len(registered), strings.Join(registered, " "))

	mux := http.NewServeMux()

	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return srv },
		nil,
	)
	mux.Handle("/mcp", mcpHandler)

	// /healthz never depends on n8n reachability — if the binary is
	// up, this returns OK. n8n outages should NOT trip the readiness
	// probe (sweep dispatch fails clearly with a 502/timeout, the pod
	// stays Ready).
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})

	httpServer := &http.Server{
		Addr:              *httpAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("Listening on HTTP %s (streamable transport at /mcp, /healthz live)", *httpAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
		os.Exit(1)
	}
	log.Println("bye")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
