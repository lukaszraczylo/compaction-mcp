package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const serverInstructions = `Context compactor — manages working memory within a token budget.

At session start, call 'configure' with token_budget set to ~40% of your context window.
Example: 200K context window → token_budget = 80000.

Workflow:
- 'store' important context (always include a summary for efficient compaction)
- 'query' to retrieve stored information instead of re-reading sources
- 'status' to check budget usage
- 'compact' when usage is high — it frees space and identifies items needing summarization
- 'update' to add summaries to items flagged by compaction`

func main() {
	budget := flag.Int("budget", 100000, "Token budget for context storage")
	flag.Parse()

	budgetExplicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "budget" {
			budgetExplicit = true
		}
	})

	store := NewStore(*budget)

	hooks := &server.Hooks{}
	if !budgetExplicit {
		hooks.OnAfterInitialize = append(hooks.OnAfterInitialize,
			func(ctx context.Context, id any, req *mcp.InitializeRequest, res *mcp.InitializeResult) {
				name := strings.ToLower(req.Params.ClientInfo.Name)
				switch {
				case strings.Contains(name, "claude"):
					// Claude models: 200K context → 40% = 80K budget
					store.Configure(80000, nil, 0)
				case strings.Contains(name, "cursor"):
					store.Configure(60000, nil, 0)
				}
			},
		)
	}

	s := server.NewMCPServer(
		"compactor",
		"0.1.0",
		server.WithToolCapabilities(false),
		server.WithRecovery(),
		server.WithInstructions(serverInstructions),
		server.WithHooks(hooks),
	)

	registerTools(s, store)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}
