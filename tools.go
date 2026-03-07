package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerTools(s *server.MCPServer, store *Store) {
	s.AddTool(mcp.NewTool("store",
		mcp.WithDescription("Store a context item for later retrieval. Offload information from working context. Provide a summary for efficient compaction when budget is tight."),
		mcp.WithString("content", mcp.Required(), mcp.Description("The content to store")),
		mcp.WithString("summary", mcp.Description("Compact summary of the content (used when budget is tight)")),
		mcp.WithString("tags", mcp.Description("Comma-separated tags for categorization and retrieval")),
		mcp.WithNumber("importance", mcp.Description("Importance 1-10, default 5. Higher = harder to evict")),
	), handleStore(store))

	s.AddTool(mcp.NewTool("query",
		mcp.WithDescription("Retrieve stored context items by text search and/or tags. Returns most relevant items. Summaries returned when budget is tight."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithString("query", mcp.Description("Text to search for in stored items")),
		mcp.WithString("tags", mcp.Description("Comma-separated tags to filter by")),
		mcp.WithNumber("limit", mcp.Description("Max items to return (default 10)")),
	), handleQuery(store))

	s.AddTool(mcp.NewTool("status",
		mcp.WithDescription("Check context budget usage: total budget, tokens used, item count, usage percentage."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	), handleStatus(store))

	s.AddTool(mcp.NewTool("compact",
		mcp.WithDescription("Trigger context compaction. Promotes summaries, deduplicates similar items, evicts lowest-scoring items. Returns suggestions for items needing LLM summarization."),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithNumber("target_usage", mcp.Description("Target usage ratio 0.0-1.0 (default 0.7)")),
	), handleCompact(store))

	s.AddTool(mcp.NewTool("pin",
		mcp.WithDescription("Pin a context item to prevent automatic eviction during compaction."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("id", mcp.Required(), mcp.Description("Item ID to pin")),
	), handlePin(store))

	s.AddTool(mcp.NewTool("forget",
		mcp.WithDescription("Remove a context item from storage."),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("id", mcp.Required(), mcp.Description("Item ID to remove")),
	), handleForget(store))

	s.AddTool(mcp.NewTool("configure",
		mcp.WithDescription("Configure context budget and auto-compaction settings."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithNumber("token_budget", mcp.Description("Total token budget for storage")),
		mcp.WithBoolean("auto_compact", mcp.Description("Enable/disable auto-compaction")),
		mcp.WithNumber("auto_compact_threshold", mcp.Description("Auto-compact triggers at this usage ratio 0.0-1.0")),
	), handleConfigure(store))

	s.AddTool(mcp.NewTool("update",
		mcp.WithDescription("Add or update the summary of an existing item. Use after compaction suggests items that need summarization."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("id", mcp.Required(), mcp.Description("Item ID to update")),
		mcp.WithString("summary", mcp.Required(), mcp.Description("New summary for the item")),
	), handleUpdate(store))
}

func handleStore(store *Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content, err := req.RequireString("content")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		summary := req.GetString("summary", "")
		tagsStr := req.GetString("tags", "")
		importance := req.GetInt("importance", 5)

		var tags []string
		if tagsStr != "" {
			for _, t := range strings.Split(tagsStr, ",") {
				if t = strings.TrimSpace(t); t != "" {
					tags = append(tags, t)
				}
			}
		}

		item := store.Add(content, summary, tags, importance)
		budget, used, count, usage := store.Status()

		return mcp.NewToolResultText(fmt.Sprintf(
			"Stored [%s] (%d tokens)\nBudget: %d/%d tokens (%.0f%%), %d items",
			item.ID, item.Tokens, used, budget, usage*100, count,
		)), nil
	}
}

func handleQuery(store *Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := req.GetString("query", "")
		tagsStr := req.GetString("tags", "")
		limit := req.GetInt("limit", 10)

		var tags []string
		if tagsStr != "" {
			for _, t := range strings.Split(tagsStr, ",") {
				if t = strings.TrimSpace(t); t != "" {
					tags = append(tags, t)
				}
			}
		}

		items := store.Query(query, tags, limit)
		if len(items) == 0 {
			return mcp.NewToolResultText("No matching items found."), nil
		}

		tight := store.BudgetTight()
		var sb strings.Builder
		fmt.Fprintf(&sb, "Found %d items", len(items))
		if tight {
			sb.WriteString(" (budget tight, showing summaries where available)")
		}
		sb.WriteString(":\n\n")

		for _, item := range items {
			fmt.Fprintf(&sb, "[%s] importance:%d tokens:%d", item.ID, item.Importance, item.Tokens)
			if item.Pinned {
				sb.WriteString(" PINNED")
			}
			sb.WriteString("\n")
			if len(item.Tags) > 0 {
				fmt.Fprintf(&sb, "Tags: %s\n", strings.Join(item.Tags, ", "))
			}
			sb.WriteString("---\n")
			if tight && item.Summary != "" {
				sb.WriteString(item.Summary)
			} else {
				sb.WriteString(item.Content)
			}
			sb.WriteString("\n\n")
		}

		return mcp.NewToolResultText(sb.String()), nil
	}
}

func handleStatus(store *Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		budget, used, count, usage := store.Status()
		return mcp.NewToolResultText(fmt.Sprintf(
			"Budget: %d/%d tokens (%.1f%%)\nItems: %d\nAuto-compact: %v (threshold: %.0f%%)",
			used, budget, usage*100, count,
			store.AutoCompact(), store.AutoCompactThreshold()*100,
		)), nil
	}
}

func handleCompact(store *Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		target := req.GetFloat("target_usage", 0.7)
		if target <= 0 || target > 1.0 {
			target = 0.7
		}

		result := store.Compact(target)

		var sb strings.Builder
		fmt.Fprintf(&sb, "Compaction complete:\n")
		fmt.Fprintf(&sb, "- Evicted: %d items\n", result.Evicted)
		fmt.Fprintf(&sb, "- Summary promoted: %d items\n", result.Summarized)
		fmt.Fprintf(&sb, "- Deduplicated: %d pairs\n", result.Deduplicated)
		fmt.Fprintf(&sb, "- Tokens freed: %d\n", result.TokensFreed)
		fmt.Fprintf(&sb, "- Budget: %d → %d tokens\n", result.TokensBefore, result.TokensAfter)

		if len(result.NeedsSummary) > 0 {
			sb.WriteString("\nItems that would benefit from summarization:\n")
			for _, item := range result.NeedsSummary {
				preview := item.Content
				if len(preview) > 80 {
					preview = preview[:80] + "..."
				}
				fmt.Fprintf(&sb, "- [%s] %d tokens: %q\n", item.ID, item.Tokens, preview)
			}
			sb.WriteString("\nUse 'update' tool to add summaries to these items.")
		}

		return mcp.NewToolResultText(sb.String()), nil
	}
}

func handlePin(store *Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if store.Pin(id) {
			return mcp.NewToolResultText(fmt.Sprintf("Pinned [%s]", id)), nil
		}
		return mcp.NewToolResultError(fmt.Sprintf("Item [%s] not found", id)), nil
	}
}

func handleForget(store *Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if store.Remove(id) {
			_, used, count, usage := store.Status()
			return mcp.NewToolResultText(fmt.Sprintf(
				"Removed [%s]. Budget: %d tokens (%.0f%%), %d items",
				id, used, usage*100, count,
			)), nil
		}
		return mcp.NewToolResultError(fmt.Sprintf("Item [%s] not found", id)), nil
	}
}

func handleConfigure(store *Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		budget := req.GetInt("token_budget", 0)
		threshold := req.GetFloat("auto_compact_threshold", 0)

		// Only set auto_compact if explicitly provided
		var autoCompact *bool
		if args := req.GetArguments(); args != nil {
			if _, ok := args["auto_compact"]; ok {
				v := req.GetBool("auto_compact", true)
				autoCompact = &v
			}
		}

		store.Configure(budget, autoCompact, threshold)

		b, u, c, usg := store.Status()
		return mcp.NewToolResultText(fmt.Sprintf(
			"Configuration updated.\nBudget: %d/%d tokens (%.1f%%), %d items\nAuto-compact: %v (threshold: %.0f%%)",
			u, b, usg*100, c,
			store.AutoCompact(), store.AutoCompactThreshold()*100,
		)), nil
	}
}

func handleUpdate(store *Store) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		summary, err := req.RequireString("summary")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if store.UpdateSummary(id, summary) {
			return mcp.NewToolResultText(fmt.Sprintf("Updated summary for [%s]", id)), nil
		}
		return mcp.NewToolResultError(fmt.Sprintf("Item [%s] not found", id)), nil
	}
}
