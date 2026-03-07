# compactor

MCP server that manages LLM working memory within a token budget. Stores, retrieves, and compacts context so conversations stay under limit without losing valuable information.

Designed to complement long-term memory tools (like claude-mnemonic) by handling short-term session context.

## Install

```sh
go build -o compactor .
```

Single binary, no external dependencies. ~6 MiB.

## Usage

```sh
# Ephemeral (in-memory, default)
compactor

# With persistent state
compactor --state-dir ~/.local/share/compactor

# Explicit token budget
compactor --budget 80000
```

### Claude Code

`.claude/settings.json`:
```json
{
  "mcpServers": {
    "compactor": {
      "command": "/path/to/compactor",
      "args": ["--state-dir", "/tmp/compactor-state"]
    }
  }
}
```

### Cursor / other MCP clients

Same pattern. The server auto-detects the client and sets a reasonable budget:
- **Claude** clients: 80K tokens (40% of 200K context)
- **Cursor**: 60K tokens
- Override with `--budget` flag

## Tools

| Tool | Description |
|------|-------------|
| `recall` | **Call first every session.** Restores previous context — returns budget status + top items by relevance |
| `store` | Store content with optional summary, tags, and importance (1-10) |
| `query` | BM25-ranked search by text and/or tag filtering |
| `status` | Check budget usage, item count, auto-compact settings |
| `compact` | Trigger compaction to a target usage ratio |
| `update` | Add/update summary for an item (post-compaction workflow) |
| `pin` / `unpin` | Protect items from eviction |
| `forget` | Remove a specific item |
| `list` | Paginated item listing (newest first) |
| `bulk_store` | Store multiple items in one call (JSON array) |
| `export` | Export all items, optionally as summaries |
| `configure` | Adjust budget, auto-compact toggle and threshold |

## How compaction works

Three-phase pipeline, triggered automatically at 90% budget or manually via `compact`:

1. **Summary promotion** - Replaces content with its summary (lowest-scored items first)
2. **Deduplication** - Merges items with >70% word overlap (Jaccard similarity), keeping the higher-scored item
3. **Eviction** - Removes lowest-scored items until target usage is reached

After compaction, items without summaries are flagged. The LLM can then generate summaries via `update` for future compaction cycles.

## Scoring

Each item gets a retention score combining four signals:

```
score = 0.4 * importance + 0.3 * recency + 0.2 * access - 0.1 * size_penalty
```

**Content-type awareness** adjusts scoring automatically:

| Type | Detection | Score multiplier | Decay half-life |
|------|-----------|-----------------|-----------------|
| Error | `error:`, `panic:`, stack traces | 1.5x | 30 min |
| Decision | "decided", "going with", "approach:" | 1.3x | 6 hours |
| Code | `func`, `class`, backtick fences | 1.2x | 6 hours |
| Prose | Default | 1.0x | 2 hours |
| Tool output | `$ ` prefix, table chars | 0.7x | 15 min |

Pinned items are never evicted.

## Search

Full-text search uses BM25 ranking (k1=1.2, b=0.75) with:
- camelCase and snake_case token splitting
- 5x score boost for tag matches
- Combined BM25 relevance + item retention score

## Auto-tagging

When no tags are provided, items are automatically tagged based on content:
- Content type (error, code, decision, tool-output)
- File extensions (.go, .ts, .py, etc.)
- Infrastructure keywords (kubernetes, docker, cilium, postgres, etc.)
- URL presence (tagged as "reference")

## Persistence

With `--state-dir`, state is saved as atomic JSON every 30 seconds (when dirty) and on graceful shutdown. Without it, storage is ephemeral per session.

## CLI flags

| Flag | Default | Description |
|------|---------|-------------|
| `--budget` | `100000` | Token budget (overrides auto-detection) |
| `--state-dir` | `""` | Persistent state directory (empty = ephemeral) |

## Making it seamless

The compactor is a tool the LLM must actively use — it doesn't intercept context automatically. To make usage habitual, add this to your `CLAUDE.md`:

```markdown
## Working Memory (compactor MCP)
- At session start, ALWAYS call `recall` to restore previous context
- After making decisions, reading key files, or encountering errors: call `store` with a summary
- Before re-reading a file: call `query` to check if it's already stored
- When `status` shows >80% usage: call `compact`, then `update` items it flags
- Pin architecture decisions and user preferences with `pin`
```

The server also sends instructions via the MCP handshake that guide the LLM, but CLAUDE.md rules are stronger because they're treated as hard requirements.

### How the three layers work together

1. **MCP server instructions** — injected at connection time, tell the LLM the workflow
2. **CLAUDE.md rules** — persistent across sessions, override default behavior
3. **`recall` tool** — gives the LLM a single action to restore context, reducing friction from 12 tools to 1 entry point

With persistence (`--state-dir`), context survives across sessions. The LLM calls `recall` → gets back its stored decisions, errors, code snippets → continues where it left off.

## Architecture

```
main.go       - Entry point, CLI flags, MCP server setup, persistence wiring
store.go      - Core store: items, scoring, compaction, BM25 integration
tools.go      - MCP tool definitions and handlers
index.go      - BM25 inverted index with tag boosting
content.go    - Content type detection and auto-tagging
persist.go    - Atomic JSON persistence with background save
tokens.go     - Token count estimation (~4 chars/token)
```

## License

Private.
