# Devlog: Cozyroom AI Agent Module — 9 Pitfalls

Real bugs and hard-won lessons encountered while building the AI Agent module. If you are working in this codebase, read this before writing a line of code.

---

## Issue 1: nginx not proxying `/mcp`

**What broke:** Requests to `/mcp` returned 404 HTML instead of hitting the backend.

**Why:** The SPA fallback `location /` with `try_files` was defined before the proxy block for `/mcp`. nginx matched the wildcard first, tried to serve a static file, found nothing, and returned 404.

**Fix:**
```nginx
# CORRECT order — specific location must come first
location /mcp {
    proxy_pass http://backend:8080;
}
location / {
    try_files $uri $uri/ /index.html;
}
```

> **Lesson:** In nginx, specific `location` blocks must come BEFORE the SPA wildcard fallback. Order is not cosmetic — it determines which block wins.

---

## Issue 2: `play_track` tool calling a non-existent method

**What broke:** The `play_track` tool handler called `player.playTrackById(id)`, which does not exist on `PlayerContext`. The UI silently did nothing.

**Why:** The method was assumed to exist without verifying the actual `PlayerContext` interface. The real API only exposes `play(t: Track)`.

**Fix:** Construct a minimal `Track` object from the action payload fields before calling `play()`:
```ts
player.play({
    id: action.id,
    album_id: action.album_id,
    title: action.title,
    track_num: 0,
    duration_s: 0,
    artist_name: action.artist_name,
});
```

> **Lesson:** Always verify the actual interface/API of your framework before writing tool handlers. Do not assume method names.

---

## Issue 3: Vietnamese/Unicode search silently returning no results

**What broke:** Searching for Vietnamese text returned nothing even though matching tracks existed in the database.

**Why:** SQLite's `LIKE` operator is ASCII-only case-insensitive. It does not handle Unicode case folding — Vietnamese characters with diacritics are treated as completely different strings when the case differs.

**Fix:** Pass three LIKE variants in an OR clause, using Go's Unicode-aware `strings.ToLower` and `strings.ToUpper`:
```go
lower := strings.ToLower(query)
upper := strings.ToUpper(query)
// original mixed case also included
sql := "... WHERE title LIKE ? OR title LIKE ? OR title LIKE ?"
args := []any{"%" + lower + "%", "%" + upper + "%", "%" + query + "%"}
```

Apply the same pattern in the `recall()` memory tool and any other SQLite full-text search.

> **Lesson:** SQLite LIKE is NOT Unicode-safe. Any app with non-ASCII content (Vietnamese, CJK, Arabic, etc.) must handle case folding in Go before building the query.

---

## Issue 4: Package-level constant used after converting to a method

**What broke:** Build error — provider structs still referenced `aiSystemPromptBase` (old constant) after it was refactored to a method `aiSystemPrompt()`.

**Why:** The refactor changed the binding site but did not update all consumers.

**Fix:** Add a `systemPrompt string` field and a `SetSystemPrompt(s string)` method to each provider struct. Call it once per request in the chat handler:
```go
type openRouterProvider struct {
    model        string
    systemPrompt string
}

func (p *openRouterProvider) SetSystemPrompt(s string) {
    p.systemPrompt = s
}

// In the chat handler:
provider.SetSystemPrompt(h.aiSystemPrompt())
```

> **Lesson:** System prompt is request-scoped state. It belongs on the provider instance, not as a package global or constant.

---

## Issue 5: New DB columns missing from SELECT query

**What broke:** `tokens_in` and `tokens_out` were added to the `chat_logs` table but the `/api/ai/logs` endpoint returned zeros for both fields.

**Why:** The `SELECT` query and the `logEntry` struct were not updated when the columns were added.

**Fix:** Update the SELECT to include all columns and add the corresponding struct fields:
```go
rows, err := db.Query(
    "SELECT id, ts, model, role, content, tokens_in, tokens_out FROM chat_logs ORDER BY ts DESC LIMIT 100",
)

type logEntry struct {
    ID        int64  `json:"id"`
    Ts        string `json:"ts"`
    Model     string `json:"model"`
    Role      string `json:"role"`
    Content   string `json:"content"`
    TokensIn  int    `json:"tokens_in"`
    TokensOut int    `json:"tokens_out"`
}
```

> **Lesson:** When adding columns to a DB table, grep for every query touching that table. Update the SELECT list and the corresponding struct together.

---

## Issue 6: Cover image stuck at `opacity: 0` after AI-triggered playback

**What broke:** After the AI played a track, the album cover was invisible. Manual playback was fine; only AI-triggered plays were broken.

**Why (three-part chain):**
1. `play_track` tool returned `album_id: ""` (empty string).
2. Cover URL became `/api/covers/?w=512` — a 404.
3. The `onError` handler set `style="opacity: 0"` on the `<img>`.
4. After fixing the `album_id` bug, the inline `opacity: 0` persisted because no `onLoad` handler existed to reset it. The happy path never fired `onError`, so the residual inline style was never cleared.

**Fix — three parts:**

Part 1 — query `album_id` from the DB inside the tool handler:
```go
var albumID string
db.QueryRow("SELECT album_id FROM tracks WHERE id = ?", trackID).Scan(&albumID)
```

Part 2 — pass `album_id` through the action pipeline in `ai.go` and use it in the frontend Track construction.

Part 3 — add `onLoad` to reset opacity:
```tsx
<img
    src={coverUrl}
    onLoad={e => (e.target as HTMLImageElement).style.opacity = "1"}
    onError={e => (e.target as HTMLImageElement).style.opacity = "0"}
/>
```

> **Lesson:** Always add both `onLoad` AND `onError` when using the opacity fade trick. A missing `onLoad` is invisible on the happy path but silently breaks recovery from any prior error state.

---

## Issue 7: Free OpenRouter models rate-limiting on first request

**What broke:** The default model (`deepseek-v4-flash:free`) returned HTTP 429 on nearly every request under even light load.

**Why:** OpenRouter free-tier models share rate limits globally across all users. Under any real usage they hit the cap immediately.

**Fix:** Implement a fallback chain — try free models first, fall back to cheap paid models only if all free ones return 429:
```go
var modelFallbackChain = []string{
    "deepseek/deepseek-v4-flash:free",
    "google/gemma-4-31b-it:free",
    "nvidia/nemotron-3-super-120b-a12b:free",
    "google/gemma-4-26b-a4b-it:free",
    "inclusionai/ling-2.6-flash",  // $0.01/1M
    "qwen/qwen3.5-9b",             // $0.02/1M
    "deepseek/deepseek-v4-flash",  // $0.04/1M
}

// Update p.model when fallback succeeds so ModelID() reports the actual model used:
p.model = fallbackModel
```

> **Lesson:** Never use a single free model as the default in production. Build a fallback chain from day one. Free-tier rate limits are shared globally and will fire on your very first real user.

---

## Issue 8: Raw JSON error body displayed to the user on 429

**What broke:** On rate-limit errors, users saw raw OpenRouter JSON instead of a friendly message.

**Why:** The handler used `fmt.Sprintf("AI error: %v", err)` and returned it directly as the assistant message.

**Fix:** Detect 429 in the error string and return a clean message:
```go
errStr := err.Error()
if strings.Contains(errStr, "429") {
    return "Model is rate-limited. Try again in a few seconds."
}
return fmt.Sprintf("AI error: %v", err)
```

> **Lesson:** Never bubble raw API error responses to end users. Detect known error codes and return human-readable messages.

---

## Issue 9: High input token count (~3000) per request

**What broke:** Each chat request consumed ~3000 input tokens, making paid fallback models expensive.

**Why (two causes):**
1. All 17 tool schemas included verbose `description` fields on every property — these are sent to the model with every single request.
2. The full conversation history was appended each turn with no cap.

**Fix:**

Part 1 — strip property-level descriptions from InputSchema (keep only `type`):
```go
// Before
"id": {"type": "string", "description": "The unique track ID to play"}

// After
"id": {"type": "string"}
```

Part 2 — cap history at 8 turns server-side before passing to the provider:
```go
const maxHistory = 8
if len(history) > maxHistory {
    history = history[len(history)-maxHistory:]
}
```

Result: ~3000 tokens reduced to ~900 per request.

> **Lesson:** Tool schemas are tokens. Every property-level `description` across 17 tools adds up on every request. Cap conversation history server-side — the client should not control how much context you pay for.
