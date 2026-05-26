package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Rheinmir/integrated-agent-chat/internal/mcp"
)

const aiSystemPromptBase = `You are a helpful AI assistant with persistent memory.
You can remember facts about the user using the remember() tool,
search memory with recall(), and delete facts with forget().
After every tool call, ALWAYS write one sentence reporting the result to the user.
Never return an empty response.`

func (h *AIHandlers) aiSystemPrompt() string {
	if h.DB == nil {
		return aiSystemPromptBase
	}
	rows, err := h.DB.Query(
		`SELECT key, value FROM agent_memory ORDER BY updated_at DESC LIMIT 8`)
	if err != nil {
		return aiSystemPromptBase
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		parts = append(parts, k+": "+v)
	}
	if len(parts) == 0 {
		return aiSystemPromptBase
	}
	return aiSystemPromptBase + "\n\nWhat you remember about the user:\n- " + strings.Join(parts, "\n- ")
}

type AIHandlers struct {
	AnthropicKey  string
	GeminiKey     string
	OpenRouterKey string
	Tools         []mcp.Tool
	DB            *sql.DB
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Message string        `json:"message"`
	History []ChatMessage `json:"history"`
	Model   string        `json:"model"`
}

type chatResponse struct {
	Text      string           `json:"text"`
	Actions   []map[string]any `json:"actions"`
	Model     string           `json:"model"`
	Provider  string           `json:"provider"`
	TokensIn  int              `json:"tokens_in"`
	TokensOut int              `json:"tokens_out"`
}

type toolCall struct {
	ID    string
	Name  string
	Input map[string]any
}

// aiProvider is the single interface all providers must implement.
// msgs is opaque state passed through the agentic loop — each provider owns its message format.
type aiProvider interface {
	initMessages(history []ChatMessage, userMsg string) any
	call(msgs any, tools []mcp.Tool) (text string, calls []toolCall, tokIn int, tokOut int, done bool, err error)
	appendAssistant(msgs any, text string, calls []toolCall) any
	appendToolResults(msgs any, calls []toolCall, results []string) any
	ModelID() string
	Provider() string
	SetSystemPrompt(s string)
}

// toolStatusMessages maps tool names to short live-status strings shown in the UI.
// Customise for your domain.
var toolStatusMessages = map[string]string{
	"remember": "Saving to memory...",
	"recall":   "Searching memory...",
	"forget":   "Removing from memory...",
}

// POST /api/ai/chat — blocking JSON response
func (h *AIHandlers) chat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}

	provider, err := h.selectProvider(req.Model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	provider.SetSystemPrompt(h.aiSystemPrompt())

	byName := make(map[string]*mcp.Tool, len(h.Tools))
	for i := range h.Tools {
		byName[h.Tools[i].Name] = &h.Tools[i]
	}

	hist := req.History
	if len(hist) > 8 {
		hist = hist[len(hist)-8:]
	}

	msgs := provider.initMessages(hist, req.Message)
	var finalText string
	var actions []map[string]any
	var totalIn, totalOut int

	for i := 0; i < 6; i++ {
		text, calls, tokIn, tokOut, done, callErr := provider.call(msgs, h.Tools)
		if callErr != nil {
			errStr := callErr.Error()
			if strings.Contains(errStr, "429") {
				http.Error(w, "Model is rate-limited. Try again in a few seconds.", http.StatusTooManyRequests)
			} else {
				http.Error(w, fmt.Sprintf("AI error: %v", callErr), http.StatusInternalServerError)
			}
			return
		}
		totalIn += tokIn
		totalOut += tokOut
		if text != "" {
			finalText = text
		}
		if done || len(calls) == 0 {
			break
		}

		results := make([]string, len(calls))
		for j, tc := range calls {
			tool, found := byName[tc.Name]
			if !found {
				results[j] = fmt.Sprintf(`{"error":"tool %s not found"}`, tc.Name)
				continue
			}
			result, toolErr := tool.Handler(tc.Input)
			if toolErr != nil {
				results[j] = fmt.Sprintf(`{"error":"%s"}`, toolErr.Error())
				continue
			}
			if rm, ok := result.(map[string]any); ok {
				if action, ok := rm["_frontend_action"].(string); ok {
					actions = append(actions, map[string]any{
						"type": action,
						"id":   rm["id"],
					})
				}
			}
			b, _ := json.Marshal(result)
			results[j] = string(b)
		}

		msgs = provider.appendAssistant(msgs, text, calls)
		msgs = provider.appendToolResults(msgs, calls, results)
	}

	if finalText == "" {
		finalText = "Done!"
	}

	go h.saveLog(req.Message, finalText, provider.ModelID(), provider.Provider(), totalIn, totalOut)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(chatResponse{
		Text:      finalText,
		Actions:   actions,
		Model:     provider.ModelID(),
		Provider:  provider.Provider(),
		TokensIn:  totalIn,
		TokensOut: totalOut,
	})
}

// POST /api/ai/chat/stream — Server-Sent Events with live status updates
func (h *AIHandlers) chatStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		send(map[string]any{"error": "invalid request"})
		return
	}
	if req.Message == "" {
		send(map[string]any{"error": "message required"})
		return
	}

	provider, err := h.selectProvider(req.Model)
	if err != nil {
		send(map[string]any{"error": err.Error()})
		return
	}
	provider.SetSystemPrompt(h.aiSystemPrompt())

	// Wire live status into the OpenRouter fallback chain.
	if orP, ok := provider.(*openRouterProvider); ok {
		orP.onStatus = func(s string) { send(map[string]any{"status": s}) }
	} else {
		send(map[string]any{"status": "Connecting to model..."})
	}

	hist := req.History
	if len(hist) > 8 {
		hist = hist[len(hist)-8:]
	}

	byName := make(map[string]*mcp.Tool, len(h.Tools))
	for i := range h.Tools {
		byName[h.Tools[i].Name] = &h.Tools[i]
	}

	msgs := provider.initMessages(hist, req.Message)
	var finalText string
	var actions []map[string]any
	var totalIn, totalOut int

	for i := 0; i < 6; i++ {
		text, calls, tokIn, tokOut, done, callErr := provider.call(msgs, h.Tools)
		if callErr != nil {
			if strings.Contains(callErr.Error(), "429") {
				send(map[string]any{"error": "Model is rate-limited. Try again in a few seconds."})
			} else {
				send(map[string]any{"error": fmt.Sprintf("AI error: %v", callErr)})
			}
			return
		}
		totalIn += tokIn
		totalOut += tokOut
		if text != "" {
			finalText = text
		}
		if done || len(calls) == 0 {
			break
		}

		results := make([]string, len(calls))
		for j, tc := range calls {
			if s, ok := toolStatusMessages[tc.Name]; ok {
				send(map[string]any{"status": s})
			}
			tool, found := byName[tc.Name]
			if !found {
				results[j] = fmt.Sprintf(`{"error":"tool %s not found"}`, tc.Name)
				continue
			}
			result, toolErr := tool.Handler(tc.Input)
			if toolErr != nil {
				results[j] = fmt.Sprintf(`{"error":"%s"}`, toolErr.Error())
				continue
			}
			if rm, ok := result.(map[string]any); ok {
				if action, ok := rm["_frontend_action"].(string); ok {
					actions = append(actions, map[string]any{
						"type": action,
						"id":   rm["id"],
					})
				}
			}
			b, _ := json.Marshal(result)
			results[j] = string(b)
		}
		msgs = provider.appendAssistant(msgs, text, calls)
		msgs = provider.appendToolResults(msgs, calls, results)
	}

	if finalText == "" {
		finalText = "Done!"
	}

	go h.saveLog(req.Message, finalText, provider.ModelID(), provider.Provider(), totalIn, totalOut)

	send(map[string]any{
		"text":       finalText,
		"actions":    actions,
		"model":      provider.ModelID(),
		"provider":   provider.Provider(),
		"tokens_in":  totalIn,
		"tokens_out": totalOut,
	})
}

// GET /api/ai/memory
func (h *AIHandlers) memoryList(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		http.Error(w, "no db", http.StatusServiceUnavailable)
		return
	}
	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT key, value, updated_at FROM agent_memory ORDER BY updated_at DESC`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type fact struct {
		Key       string `json:"key"`
		Value     string `json:"value"`
		UpdatedAt string `json:"updated_at"`
	}
	var facts []fact
	for rows.Next() {
		var f fact
		rows.Scan(&f.Key, &f.Value, &f.UpdatedAt)
		facts = append(facts, f)
	}
	if facts == nil {
		facts = []fact{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"facts": facts})
}

// PUT /api/ai/memory — bulk replace
func (h *AIHandlers) memoryImport(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		http.Error(w, "no db", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Facts []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"facts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	tx.Exec(`DELETE FROM agent_memory`)
	for _, f := range body.Facts {
		if strings.TrimSpace(f.Key) == "" {
			continue
		}
		tx.Exec(`INSERT INTO agent_memory (key, value, updated_at) VALUES (?, ?, ?)`,
			strings.TrimSpace(f.Key), f.Value, time.Now().UTC().Format(time.RFC3339))
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "imported": len(body.Facts)})
}

// DELETE /api/ai/memory/{key}
func (h *AIHandlers) memoryDelete(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		http.Error(w, "no db", http.StatusServiceUnavailable)
		return
	}
	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "key required", http.StatusBadRequest)
		return
	}
	h.DB.ExecContext(r.Context(), `DELETE FROM agent_memory WHERE key = ?`, key)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// GET /api/ai/logs
func (h *AIHandlers) logs(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		http.Error(w, "no db", http.StatusServiceUnavailable)
		return
	}
	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT id, ts, model, provider, user_msg, reply, tok_in, tok_out, failure
		 FROM chat_logs ORDER BY ts DESC LIMIT 100`)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type logEntry struct {
		ID       int64  `json:"id"`
		TS       string `json:"ts"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
		UserMsg  string `json:"user_msg"`
		Reply    string `json:"reply"`
		TokIn    int    `json:"tok_in"`
		TokOut   int    `json:"tok_out"`
		Failure  bool   `json:"failure"`
	}
	var entries []logEntry
	for rows.Next() {
		var e logEntry
		var failureInt int
		rows.Scan(&e.ID, &e.TS, &e.Model, &e.Provider, &e.UserMsg, &e.Reply, &e.TokIn, &e.TokOut, &failureInt)
		e.Failure = failureInt == 1
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []logEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

func (h *AIHandlers) saveLog(userMsg, reply, model, provider string, tokIn, tokOut int) {
	if h.DB == nil {
		return
	}
	h.DB.Exec(
		`INSERT INTO chat_logs(model,provider,user_msg,reply,tok_in,tok_out,failure) VALUES(?,?,?,?,?,?,0)`,
		model, provider, userMsg, reply, tokIn, tokOut,
	)
}

// selectProvider picks a provider based on model name prefix and available API keys.
func (h *AIHandlers) selectProvider(model string) (aiProvider, error) {
	if model == "" {
		switch {
		case h.AnthropicKey != "":
			return &anthropicProvider{key: h.AnthropicKey, model: "claude-haiku-4-5-20251001"}, nil
		case h.GeminiKey != "":
			return &geminiProvider{key: h.GeminiKey, model: "gemini-2.0-flash"}, nil
		case h.OpenRouterKey != "":
			return &openRouterProvider{key: h.OpenRouterKey, model: "deepseek/deepseek-v4-flash:free"}, nil
		default:
			return nil, fmt.Errorf("no AI API key configured (set ANTHROPIC_API_KEY, GEMINI_API_KEY, or OPENROUTER_API_KEY)")
		}
	}

	switch {
	case strings.HasPrefix(model, "claude-"):
		if h.AnthropicKey == "" {
			return nil, fmt.Errorf("ANTHROPIC_API_KEY not set")
		}
		return &anthropicProvider{key: h.AnthropicKey, model: model}, nil
	case strings.HasPrefix(model, "gemini-"):
		if h.GeminiKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY not set")
		}
		return &geminiProvider{key: h.GeminiKey, model: model}, nil
	default:
		if h.OpenRouterKey == "" {
			return nil, fmt.Errorf("OPENROUTER_API_KEY not set for model %q", model)
		}
		return &openRouterProvider{key: h.OpenRouterKey, model: model}, nil
	}
}
