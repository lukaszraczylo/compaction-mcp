package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const serverInstructions = `Context compactor — your working memory. Use it to avoid losing information when your context window compresses.

MANDATORY: Call 'recall' at the start of every session to restore previous context.

WHEN TO STORE (call 'store' with a summary):
- After making a decision or choosing an approach
- After encountering and understanding an error
- After reading a file you'll need to reference later
- After the user explains requirements or constraints
- Before your context is likely to compress (long sessions, large outputs)

WHEN TO QUERY (call 'query' instead of re-reading):
- Before reading a file you may have stored previously
- When you need to recall a decision, error, or requirement
- When the user references something from earlier in the session

WHEN TO COMPACT (call 'compact'):
- When 'status' shows >80% budget usage
- After 'compact', use 'update' to summarize items it flags

TIPS:
- Always include a summary when storing — enables efficient compaction
- Tag items for easy retrieval: error, decision, code, requirement
- Pin critical items (architecture decisions, user preferences) with 'pin'
- Higher importance (7-10) for decisions and requirements, lower (1-4) for tool output`

func main() {
	budget := flag.Int("budget", 100000, "Token budget for context storage")
	stateDir := flag.String("state-dir", "", "Directory for persistent state (empty = ephemeral)")
	flag.Parse()

	budgetExplicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "budget" {
			budgetExplicit = true
		}
	})

	store := NewStore(*budget)

	var persister *Persister
	if *stateDir != "" {
		var err error
		persister, err = NewPersister(*stateDir, store)
		if err != nil {
			fmt.Fprintf(os.Stderr, "persistence error: %v\n", err)
			os.Exit(1)
		}
		if err := persister.Load(); err != nil {
			fmt.Fprintf(os.Stderr, "load state error: %v\n", err)
			os.Exit(1)
		}
		persister.Start(30 * time.Second)
	}

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

	if persister != nil {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigCh
			persister.Stop()
			os.Exit(0)
		}()
	}

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		if persister != nil {
			persister.Stop()
		}
		os.Exit(1)
	}
	if persister != nil {
		persister.Stop()
	}
}
