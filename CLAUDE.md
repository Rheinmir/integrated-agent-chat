# Agent Onboarding -- Integrated Agent Chat

This file is for an AI agent reading this repo. After reading this you should be able to:
1. Understand the full architecture
2. Know exactly which files to edit for common changes
3. Run the project locally
4. Add a new tool in under 5 minutes

---

## Architecture at a Glance

```
HTTP Request (POST /api/ai/chat)
        |
        v
   api/ai.go  chat()
        |  1. load memory -> build system prompt
        |  2. selectProvider(model) -> aiProvider
        |  loop (max 6):
        |    3. provider.call() -> (text, toolCalls, tokIn, tokOut, done)
        |    4. if done -> break
        |    5. execute each toolCall via mcp registry
        |    6. provider.appendToolResults(results)
        |  7. saveLog(db, ...)
        |
        v
   JSON response  {reply, model, provider, tokensIn, tokensOut, iterations}
```

---

## File Reference

| File | Purpose | Edit when |
|---|---|---|
| backend/internal/mcp/registry.go | Tool definitions | Adding / removing tools |
| backend/internal/api/ai.go | Agentic loop, system prompt, memory CRUD | Changing loop, prompt, memory schema |
| backend/internal/api/ai_providers.go | Anthropic / Gemini / OpenRouter adapters | Fixing provider bugs, adding a 4th provider |
| backend/internal/api/routes.go | HTTP route wiring | Adding new routes |
| backend/internal/db/db.go | SQLite migrations | Adding new tables / columns |
| backend/main.go | Entry point | Changing port, loading new env vars |
| frontend/src/pages/AIAssistantPage.tsx | React UI | UI changes |
| frontend/src/styles/ai.css | Chat styles | Style changes |

---

## Key Interfaces

### aiProvider (backend/internal/api/ai.go)

```go
type aiProvider interface {
    initMessages(systemPrompt, userMsg string)
    call(ctx context.Context, tools []mcp.Tool) (text string, calls []toolCall, tokIn, tokOut int, done bool, err error)
    appendAssistant(text string, calls []toolCall)
    appendToolResults(results []toolResult)
    ModelID() string
    Provider() string
    SetSystemPrompt(p string)
}
```

### Tool (backend/internal/mcp/registry.go)

```go
type Tool struct {
    Name        string
    Description string
    InputSchema map[string]any  // JSON Schema object
    Handler     func(map[string]any) (any, error)
}
```

InputSchema is passed verbatim to each provider. Field-name differences
(Anthropic: input_schema, Gemini: parameters, OpenRouter: parameters)
are handled per-provider -- you never need to think about them when writing a tool.

---

## Agentic Loop

```
maxIter = 6
for i := 0; i < maxIter; i++ {
    text, calls, tokIn, tokOut, done, err = provider.call(ctx, tools)
    totalTokIn  += tokIn
    totalTokOut += tokOut

    if done || len(calls) == 0 { finalText = text; break }

    for each call:
        result = executeToolCall(call, tools)

    provider.appendAssistant(text, calls)   // add assistant turn
    provider.appendToolResults(results)      // add tool results turn
}
```

---

## Memory System

Storage: agent_memory(key TEXT PRIMARY KEY, value TEXT, updated_at DATETIME)

Built-in tools:
- remember(key, value) -- UPSERT into agent_memory
- recall(query)        -- 3-variant LIKE: exact phrase, words reversed, each word
- forget(key)          -- DELETE from agent_memory

System prompt injection (aiSystemPrompt in ai.go):
- Top 8 facts ordered by updated_at DESC
- Appended as: "Ban nho ve user:\n- key: value\n..."

---

## Provider Routing

selectProvider(model) in ai.go:
  model starts with "claude-"  -> anthropicProvider
  model starts with "gemini-"  -> geminiProvider
  anything else                -> openRouterProvider

Default model: "claude-sonnet-4-5"

---

## Provider Wire Formats (critical)

### Anthropic
- Endpoint: POST https://api.anthropic.com/v1/messages
- Tools: tools[].input_schema (NOT parameters -- this is the #1 bug)
- Tool call in response: content[].type == "tool_use" with .id, .name, .input
- Tool result: {role:"user", content:[{type:"tool_result", tool_use_id, content}]}
- done when: stop_reason == "end_turn" or "stop_sequence"

### Gemini
- Endpoint: POST https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent?key={apiKey}
- Tools: tools[0].functionDeclarations[].parameters
- Tool call in response: candidates[0].content.parts[].functionCall.{name, args}
- Tool result: {role:"user", parts:[{functionResponse:{name, response}}]}
- done when: finishReason == "STOP"

### OpenRouter
- Endpoint: POST https://openrouter.ai/api/v1/chat/completions
- Standard OpenAI format: tools[].function.{name, description, parameters}
- Tool call in response: choices[0].message.tool_calls[].{id, function.{name, arguments}}
- arguments is a JSON string -- always json.Unmarshal it
- done when: finish_reason == "stop"

---

## Environment Variables

```
ANTHROPIC_API_KEY=sk-ant-...
GEMINI_API_KEY=AIza...
OPENROUTER_API_KEY=sk-or-...
PORT=8080           # optional
DB_PATH=./data/agent.db  # optional
```

---

## Running Locally

```bash
# Terminal 1 -- backend
cd backend
export ANTHROPIC_API_KEY=sk-ant-...
go run main.go

# Terminal 2 -- frontend
cd frontend
npm install
npm run dev   # http://localhost:5173
```

---

## Adding a New Tool -- Checklist

1. Open backend/internal/mcp/registry.go
2. Write: func myTool(db *sql.DB) Tool { ... }
3. Register: tools = append(tools, myTool(db)) inside NewRegistry()
4. Done -- auto-injected into all providers

## Adding a New Provider -- Checklist

1. Open backend/internal/api/ai_providers.go
2. Create struct, implement all 7 methods of aiProvider
3. Open backend/internal/api/ai.go, add case in selectProvider()

---

## SQLite Schema

```sql
CREATE TABLE IF NOT EXISTS chat_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts DATETIME DEFAULT CURRENT_TIMESTAMP,
    model TEXT, provider TEXT, user_msg TEXT, reply TEXT,
    tok_in INTEGER DEFAULT 0, tok_out INTEGER DEFAULT 0,
    iterations INTEGER DEFAULT 1, failure INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS agent_memory (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

---

## Common Mistakes

- Anthropic tools use input_schema, not parameters -- #1 integration bug
- Gemini tool results go in parts[].functionResponse, not content[].tool_result
- OpenRouter tool_calls[].function.arguments is a JSON string -- always unmarshal
- Call appendAssistant() BEFORE appendToolResults() -- turn order matters
- Memory recall uses 3 LIKE variants for Unicode -- don't simplify to one LIKE
