# Integrated Agent Chat -- Reusable AI Agent Framework

A production-ready Go + React template implementing a multi-provider AI agent with memory, tools,
and an agentic loop. Clone this to add an AI assistant to any web app within an hour.

## What's Inside

- **3 AI providers** -- Anthropic (Claude), Google Gemini, OpenRouter -- behind a single interface
- **Agentic loop** -- up to 6 iterations: call -> tool_use -> execute -> append -> repeat
- **MCP tool registry** -- add tools as Go structs with a Handler func
- **Persistent memory** -- remember/recall/forget tools backed by SQLite
- **SQLite logging** -- every conversation saved to chat_logs
- **React chat UI** -- model badge, token counter, collapsible memory panel

## File Map

```
backend/
  main.go                    -- entry point (env, SQLite, HTTP server)
  go.mod
  internal/
    db/db.go                 -- open SQLite + run migrations
    api/
      routes.go              -- wire routes -> handler
      ai.go                  -- chat handler, agentic loop, memory CRUD, logs
      ai_providers.go        -- Anthropic / Gemini / OpenRouter adapters
    mcp/
      registry.go            -- Tool struct, NewRegistry(), built-in tools
frontend/
  src/
    pages/AIAssistantPage.tsx
    styles/ai.css
docker-compose.yml
Dockerfile                   -- backend multi-stage
Dockerfile.frontend          -- frontend multi-stage (nginx)
nginx.conf                   -- SPA + /api proxy
```

## 5-Minute Setup

### Prerequisites
- Go 1.22+, Node 18+, Docker (optional)

### Local (no Docker)

```bash
# 1. Backend
cd backend
cp ../.env.example .env        # fill in keys
go mod download
go run main.go                 # listens on :8080

# 2. Frontend (separate terminal)
cd frontend
npm install
VITE_API_BASE=http://localhost:8080 npm run dev
```

### Docker Compose

```bash
cp .env.example .env           # fill in ANTHROPIC_API_KEY etc.
docker-compose up --build
# open http://localhost:18080
```

## Environment Variables

| Variable | Required | Notes |
|---|---|---|
| ANTHROPIC_API_KEY | one of three | Claude models |
| GEMINI_API_KEY | one of three | Gemini models |
| OPENROUTER_API_KEY | one of three | 100+ models |
| PORT | no | default 8080 |
| DB_PATH | no | default ./data/agent.db |

## API Endpoints

| Method | Path | Description |
|---|---|---|
| POST | /api/ai/chat | Send a message, get a reply |
| GET | /api/ai/memory | List all remembered facts |
| PUT | /api/ai/memory | Import facts (JSON array) |
| DELETE | /api/ai/memory | Delete all facts |
| GET | /api/ai/logs | Recent chat logs |

## Adding a New Tool

1. Open `backend/internal/mcp/registry.go`
2. Add a function returning a Tool:

```go
func myTool(db *sql.DB) Tool {
    return Tool{
        Name:        "my_tool",
        Description: "Does something useful",
        InputSchema: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "query": map[string]any{"type": "string"},
            },
            "required": []string{"query"},
        },
        Handler: func(args map[string]any) (any, error) {
            q, _ := args["query"].(string)
            return map[string]any{"result": q}, nil
        },
    }
}
```

3. Register it in `NewRegistry()`: `tools = append(tools, myTool(db))`

## Architecture Notes

- The aiProvider interface normalises all three providers
- selectProvider() routes by model prefix: claude-* -> Anthropic, gemini-* -> Gemini, else -> OpenRouter
- Memory facts are prepended to every system prompt (top 8 by recency)
- Token counts accumulate across all loop iterations
