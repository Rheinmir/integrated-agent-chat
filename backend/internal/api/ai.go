package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Rheinmir/integrated-agent-chat/internal/mcp"
)

// aiProvider is the single interface all AI providers must implement.
type aiProvider interface {
	initMessages(systemPrompt, userMsg string)
	call(ctx context.Context, tools []mcp.Tool) (text string, calls []toolCall, tokIn, tokOut int, done bool, err error)
	appendAssistant(text string, calls []toolCall)
	appendToolResults(results []toolResult)
	ModelID() string
	Provider() string
	SetSystemPrompt(p string)
}

type toolCall struct {
	ID    string
	Name  string
	Input map[string]any
}

type toolResult struct {
	ID     string
	Name   string
	Output any
	IsErr  bool
}

// AIHandlers holds all dependencies for the AI API surface.
type AIHandlers struct {
	AnthropicKey  string
	GeminiKey     string
	OpenRouterKey string
	Tools         []mcp.Tool
	DB            *sql.DB
}

type chatRequest struct {
	Message string `json:"message"`
	Model   string `json:"model"`
}

type chatResponse struct {
	Reply      string `json:"reply"`
	Model      string `json:"model"`
	Provider   string `json:"provider"`
	TokensIn   int    `json:"tokensIn"`
	TokensOut  int    `json:"tokensOut"`
	Iterations int    `json:"iterations"`
}

type memoryEntry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updatedAt"`
}

type logEntry struct {
	ID         int64  `json:"id"`
	TS         string `json:"ts"`
	Model      string `json:"model"`
	Provider   string `json:"provider"`
	UserMsg    string `json:"userMsg"`
	Reply      string `json:"reply"`
	TokIn      int    `json:"tokIn"`
	TokOut     int    `json:"tokOut"`
	Iterations int    `json:"iterations"`
	Failure    bool   `json:"failure"`
}

func (h *AIHandlers) chat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}
	if req.Model == "" {
		req.Model = "claude-sonnet-4-5"
	}

	ctx := r.Context()
	systemPrompt := h.aiSystemPrompt(ctx)

	provider, err := h.selectProvider(req.Model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	provider.SetSystemPrompt(systemPrompt)
	provider.initMessages(systemPrompt, req.Message)

	const maxIter = 6
	var (
		finalText   string
		totalTokIn  int
		totalTokOut int
		iterations  int
	)

	for i := 0; i < maxIter; i++ {
		iterations = i + 1

		text, calls, tokIn, tokOut, done, callErr := provider.call(ctx, h.Tools)
		totalTokIn += tokIn
		totalTokOut += tokOut

		if callErr != nil {
			log.Printf("provider.call error (iter %d): %v", i, callErr)
			finalText = fmt.Sprintf("[Error calling AI provider: %v]", callErr)
			break
		}

		if done || len(calls) == 0 {
			finalText = text
			break
		}

		results := make([]toolResult, 0, len(calls))
		for _, tc := range calls {
			output, toolErr := executeToolCall(tc, h.Tools)
			tr := toolResult{ID: tc.ID, Name: tc.Name, Output: output}
			if toolErr != nil {
				tr.IsErr = true
				tr.Output = map[string]any{"error": toolErr.Error()}
			}
			results = append(results, tr)
		}

		provider.appendAssistant(text, calls)
		provider.appendToolResults(results)

		if text != "" {
			finalText = text
		}
	}

	failure := detectFailure(finalText)
	go h.saveLog(provider.ModelID(), provider.Provider(), req.Message, finalText,
		totalTokIn, totalTokOut, iterations, failure)

	resp := chatResponse{
		Reply:      finalText,
		Model:      provider.ModelID(),
		Provider:   provider.Provider(),
		TokensIn:   totalTokIn,
		TokensOut:  totalTokOut,
		Iterations: iterations,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func executeToolCall(tc toolCall, tools []mcp.Tool) (any, error) {
	for _, t := range tools {
		if t.Name == tc.Name {
			return t.Handler(tc.Input)
		}
	}
	return nil, fmt.Errorf("unknown tool: %s", tc.Name)
}

func (h *AIHandlers) selectProvider(model string) (aiProvider, error) {
	switch {
	case strings.HasPrefix(model, "claude-"):
		if h.AnthropicKey == "" {
			return nil, fmt.Errorf("ANTHROPIC_API_KEY not configured")
		}
		return newAnthropicProvider(model, h.AnthropicKey), nil
	case strings.HasPrefix(model, "gemini-"):
		if h.GeminiKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY not configured")
		}
		return newGeminiProvider(model, h.GeminiKey), nil
	default:
		if h.OpenRouterKey == "" {
			return nil, fmt.Errorf("OPENROUTER_API_KEY not configured")
		}
		return newOpenRouterProvider(model, h.OpenRouterKey), nil
	}
}

func (h *AIHandlers) aiSystemPrompt(ctx context.Context) string {
	base := `You are a helpful AI assistant with persistent memory.
You can remember facts about the user using the remember() tool,
search memory with recall(), and delete facts with forget().
Always be concise and helpful.`

	rows, err := h.DB.QueryContext(ctx,
		`SELECT key, value FROM agent_memory ORDER BY updated_at DESC LIMIT 8`)
	if err != nil {
		return base
	}
	defer rows.Close()

	var facts []string
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			facts = append(facts, fmt.Sprintf("- %s: %s", k, v))
		}
	}
	if len(facts) == 0 {
		return base
	}
	return base + "\n\nBan nho ve user:\n" + strings.Join(facts, "\n")
}

func (h *AIHandlers) memoryList(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT key, value, updated_at FROM agent_memory ORDER BY updated_at DESC`)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var entries []memoryEntry
	for rows.Next() {
		var e memoryEntry
		if rows.Scan(&e.Key, &e.Value, &e.UpdatedAt) == nil {
			entries = append(entries, e)
		}
	}
	if entries == nil {
		entries = []memoryEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

func (h *AIHandlers) memoryImport(w http.ResponseWriter, r *http.Request) {
	var entries []memoryEntry
	if err := json.NewDecoder(r.Body).Decode(&entries); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	tx, err := h.DB.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, e := range entries {
		if e.Key == "" {
			continue
		}
		ts := e.UpdatedAt
		if ts == "" {
			ts = now
		}
		_, err := tx.ExecContext(r.Context(),
			`INSERT INTO agent_memory(key,value,updated_at) VALUES(?,?,?)
			 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
			e.Key, e.Value, ts,
		)
		if err != nil {
			tx.Rollback()
			http.Error(w, "db error on import", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "commit error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"imported": len(entries)})
}

func (h *AIHandlers) memoryDelete(w http.ResponseWriter, r *http.Request) {
	if key := r.URL.Query().Get("key"); key != "" {
		res, err := h.DB.ExecContext(r.Context(), `DELETE FROM agent_memory WHERE key = ?`, key)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		n, _ := res.RowsAffected()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"deleted": n})
		return
	}

	res, err := h.DB.ExecContext(r.Context(), `DELETE FROM agent_memory`)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	n, _ := res.RowsAffected()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"deleted": n})
}

func (h *AIHandlers) logs(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.QueryContext(r.Context(),
		`SELECT id, ts, model, provider, user_msg, reply, tok_in, tok_out, iterations, failure
		 FROM chat_logs ORDER BY ts DESC LIMIT 100`)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var entries []logEntry
	for rows.Next() {
		var e logEntry
		var failureInt int
		if err := rows.Scan(&e.ID, &e.TS, &e.Model, &e.Provider,
			&e.UserMsg, &e.Reply, &e.TokIn, &e.TokOut, &e.Iterations, &failureInt); err != nil {
			continue
		}
		e.Failure = failureInt == 1
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []logEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

func (h *AIHandlers) saveLog(model, provider, userMsg, reply string,
	tokIn, tokOut, iterations int, failure bool) {
	failureInt := 0
	if failure {
		failureInt = 1
	}
	_, err := h.DB.Exec(
		`INSERT INTO chat_logs(model,provider,user_msg,reply,tok_in,tok_out,iterations,failure)
		 VALUES(?,?,?,?,?,?,?,?)`,
		model, provider, userMsg, reply, tokIn, tokOut, iterations, failureInt,
	)
	if err != nil {
		log.Printf("saveLog error: %v", err)
	}
}

func detectFailure(reply string) bool {
	lower := strings.ToLower(reply)
	for _, m := range []string{"[error", "i cannot", "i'm unable", "an error occurred", "failed to"} {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}
