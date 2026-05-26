package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Rheinmir/integrated-agent-chat/internal/mcp"
)

// ── Anthropic ─────────────────────────────────────────────────────────────────

type anthropicProvider struct {
	key          string
	model        string
	systemPrompt string
}

func (p *anthropicProvider) initMessages(history []ChatMessage, userMsg string) any {
	msgs := make([]map[string]any, 0, len(history)+1)
	for _, m := range history {
		msgs = append(msgs, map[string]any{"role": m.Role, "content": m.Content})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": userMsg})
	return msgs
}

func (p *anthropicProvider) call(msgs any, tools []mcp.Tool) (string, []toolCall, int, int, bool, error) {
	toolDefs := make([]map[string]any, len(tools))
	for i, t := range tools {
		toolDefs[i] = map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.InputSchema,
		}
	}
	body := map[string]any{
		"model":      p.model,
		"max_tokens": 4096,
		"system":     p.systemPrompt,
		"tools":      toolDefs,
		"messages":   msgs,
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(b))
	req.Header.Set("x-api-key", p.key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, 0, 0, false, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", nil, 0, 0, false, fmt.Errorf("anthropic %d: %s", resp.StatusCode, raw)
	}

	var parsed struct {
		Content    []map[string]any `json:"content"`
		StopReason string           `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", nil, 0, 0, false, err
	}

	var text string
	var calls []toolCall
	for _, block := range parsed.Content {
		switch block["type"] {
		case "text":
			if t, ok := block["text"].(string); ok {
				text = t
			}
		case "tool_use":
			id, _ := block["id"].(string)
			name, _ := block["name"].(string)
			input, _ := block["input"].(map[string]any)
			calls = append(calls, toolCall{ID: id, Name: name, Input: input})
		}
	}
	done := parsed.StopReason == "end_turn" || parsed.StopReason == "stop_sequence"
	return text, calls, parsed.Usage.InputTokens, parsed.Usage.OutputTokens, done, nil
}

func (p *anthropicProvider) appendAssistant(msgs any, text string, calls []toolCall) any {
	m := msgs.([]map[string]any)
	content := []map[string]any{}
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	for _, c := range calls {
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    c.ID,
			"name":  c.Name,
			"input": c.Input,
		})
	}
	return append(m, map[string]any{"role": "assistant", "content": content})
}

func (p *anthropicProvider) appendToolResults(msgs any, calls []toolCall, results []string) any {
	m := msgs.([]map[string]any)
	content := make([]map[string]any, len(calls))
	for i, c := range calls {
		content[i] = map[string]any{
			"type":        "tool_result",
			"tool_use_id": c.ID,
			"content":     results[i],
		}
	}
	return append(m, map[string]any{"role": "user", "content": content})
}

func (p *anthropicProvider) ModelID() string          { return p.model }
func (p *anthropicProvider) Provider() string         { return "anthropic" }
func (p *anthropicProvider) SetSystemPrompt(s string) { p.systemPrompt = s }

// ── Gemini ────────────────────────────────────────────────────────────────────

type geminiProvider struct {
	key          string
	model        string
	systemPrompt string
}

// geminiSchema converts JSON Schema (lowercase types) to Gemini format (uppercase).
func geminiSchema(s map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range s {
		switch k {
		case "type":
			if t, ok := v.(string); ok {
				out["type"] = strings.ToUpper(t)
			}
		case "properties":
			if props, ok := v.(map[string]any); ok {
				converted := map[string]any{}
				for pk, pv := range props {
					if pm, ok := pv.(map[string]any); ok {
						converted[pk] = geminiSchema(pm)
					}
				}
				out["properties"] = converted
			}
		default:
			out[k] = v
		}
	}
	return out
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string        `json:"text,omitempty"`
	FunctionCall     *geminiFnCall `json:"functionCall,omitempty"`
	FunctionResponse *geminiFnResp `json:"functionResponse,omitempty"`
}

type geminiFnCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type geminiFnResp struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

func (p *geminiProvider) initMessages(history []ChatMessage, userMsg string) any {
	contents := make([]geminiContent, 0, len(history)+1)
	for _, m := range history {
		role := m.Role
		if role == "assistant" {
			role = "model"
		}
		contents = append(contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: m.Content}},
		})
	}
	contents = append(contents, geminiContent{
		Role:  "user",
		Parts: []geminiPart{{Text: userMsg}},
	})
	return contents
}

func (p *geminiProvider) call(msgs any, tools []mcp.Tool) (string, []toolCall, int, int, bool, error) {
	contents := msgs.([]geminiContent)

	fnDecls := make([]map[string]any, len(tools))
	for i, t := range tools {
		fnDecls[i] = map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  geminiSchema(t.InputSchema),
		}
	}

	body := map[string]any{
		"system_instruction": map[string]any{
			"parts": []map[string]any{{"text": p.systemPrompt}},
		},
		"tools": []map[string]any{
			{"function_declarations": fnDecls},
		},
		"contents": contents,
	}
	b, _ := json.Marshal(body)

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", p.model, p.key)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, 0, 0, false, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", nil, 0, 0, false, fmt.Errorf("gemini %d: %s", resp.StatusCode, raw)
	}

	var parsed struct {
		Candidates []struct {
			Content      geminiContent `json:"content"`
			FinishReason string        `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", nil, 0, 0, false, err
	}
	if len(parsed.Candidates) == 0 {
		return "", nil, parsed.UsageMetadata.PromptTokenCount, parsed.UsageMetadata.CandidatesTokenCount, true, nil
	}

	cand := parsed.Candidates[0]
	var text string
	var calls []toolCall
	for _, part := range cand.Content.Parts {
		if part.Text != "" {
			text = part.Text
		}
		if part.FunctionCall != nil {
			calls = append(calls, toolCall{
				ID:    part.FunctionCall.Name,
				Name:  part.FunctionCall.Name,
				Input: part.FunctionCall.Args,
			})
		}
	}
	done := cand.FinishReason == "STOP" || len(calls) == 0
	return text, calls, parsed.UsageMetadata.PromptTokenCount, parsed.UsageMetadata.CandidatesTokenCount, done, nil
}

func (p *geminiProvider) appendAssistant(msgs any, text string, calls []toolCall) any {
	contents := msgs.([]geminiContent)
	parts := []geminiPart{}
	if text != "" {
		parts = append(parts, geminiPart{Text: text})
	}
	for _, c := range calls {
		parts = append(parts, geminiPart{
			FunctionCall: &geminiFnCall{Name: c.Name, Args: c.Input},
		})
	}
	return append(contents, geminiContent{Role: "model", Parts: parts})
}

func (p *geminiProvider) appendToolResults(msgs any, calls []toolCall, results []string) any {
	contents := msgs.([]geminiContent)
	parts := make([]geminiPart, len(calls))
	for i, c := range calls {
		var respData map[string]any
		_ = json.Unmarshal([]byte(results[i]), &respData)
		if respData == nil {
			respData = map[string]any{"result": results[i]}
		}
		parts[i] = geminiPart{
			FunctionResponse: &geminiFnResp{Name: c.Name, Response: respData},
		}
	}
	return append(contents, geminiContent{Role: "user", Parts: parts})
}

func (p *geminiProvider) ModelID() string          { return p.model }
func (p *geminiProvider) Provider() string         { return "gemini" }
func (p *geminiProvider) SetSystemPrompt(s string) { p.systemPrompt = s }

// ── OpenRouter (OpenAI-compatible) ────────────────────────────────────────────

// openRouterFallbacks is tried in order when the active model returns 429 or 5xx.
// Free models first; cheap paid models as last resort.
var openRouterFallbacks = []string{
	"deepseek/deepseek-v4-flash:free",
	"google/gemma-4-31b-it:free",
	"google/gemma-4-26b-a4b-it:free",
	"inclusionai/ling-2.6-flash",
	"qwen/qwen3.5-9b",
	"deepseek/deepseek-v4-flash",
}

type openRouterProvider struct {
	key          string
	model        string
	systemPrompt string
	onStatus     func(string) // called during fallback attempts for live UI feedback
}

func (p *openRouterProvider) initMessages(history []ChatMessage, userMsg string) any {
	msgs := make([]map[string]any, 0, len(history)+2)
	msgs = append(msgs, map[string]any{"role": "system", "content": p.systemPrompt})
	for _, m := range history {
		msgs = append(msgs, map[string]any{"role": m.Role, "content": m.Content})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": userMsg})
	return msgs
}

func (p *openRouterProvider) call(msgs any, tools []mcp.Tool) (string, []toolCall, int, int, bool, error) {
	oaiTools := make([]map[string]any, len(tools))
	for i, t := range tools {
		oaiTools[i] = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.InputSchema,
			},
		}
	}

	// Build fallback list: current model first, then remaining from chain
	candidates := []string{p.model}
	for _, fb := range openRouterFallbacks {
		if fb != p.model {
			candidates = append(candidates, fb)
		}
	}

	var raw []byte
	var lastStatus int
	for i, candidate := range candidates {
		if p.onStatus != nil {
			if i == 0 {
				p.onStatus("Connecting to model...")
			} else {
				p.onStatus(fmt.Sprintf("Previous model busy, trying %s...", shortModel(candidate)))
			}
		}
		bodyMap := map[string]any{
			"model":    candidate,
			"tools":    oaiTools,
			"messages": msgs,
		}
		b, _ := json.Marshal(bodyMap)
		req, _ := http.NewRequest(http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+p.key)
		req.Header.Set("content-type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", nil, 0, 0, false, err
		}
		raw, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		lastStatus = resp.StatusCode
		if resp.StatusCode == http.StatusOK {
			p.model = candidate
			if p.onStatus != nil {
				p.onStatus(fmt.Sprintf("Model %s accepted, executing...", shortModel(candidate)))
			}
			break
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			time.Sleep(1 * time.Second)
			continue
		}
		break
	}
	if lastStatus != http.StatusOK {
		return "", nil, 0, 0, false, fmt.Errorf("openrouter %d: %s", lastStatus, raw)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", nil, 0, 0, false, err
	}
	if len(parsed.Choices) == 0 {
		return "", nil, parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens, true, nil
	}

	choice := parsed.Choices[0]
	text := choice.Message.Content
	var calls []toolCall
	for _, tc := range choice.Message.ToolCalls {
		var input map[string]any
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
		calls = append(calls, toolCall{ID: tc.ID, Name: tc.Function.Name, Input: input})
	}
	done := choice.FinishReason == "stop" || len(calls) == 0
	return text, calls, parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens, done, nil
}

func (p *openRouterProvider) appendAssistant(msgs any, text string, calls []toolCall) any {
	m := msgs.([]map[string]any)
	msg := map[string]any{"role": "assistant", "content": text}
	if len(calls) > 0 {
		tcs := make([]map[string]any, len(calls))
		for i, c := range calls {
			args, _ := json.Marshal(c.Input)
			tcs[i] = map[string]any{
				"id":   c.ID,
				"type": "function",
				"function": map[string]any{
					"name":      c.Name,
					"arguments": string(args),
				},
			}
		}
		msg["tool_calls"] = tcs
	}
	return append(m, msg)
}

func (p *openRouterProvider) appendToolResults(msgs any, calls []toolCall, results []string) any {
	m := msgs.([]map[string]any)
	for i, c := range calls {
		m = append(m, map[string]any{
			"role":         "tool",
			"tool_call_id": c.ID,
			"content":      results[i],
		})
	}
	return m
}

func (p *openRouterProvider) ModelID() string          { return p.model }
func (p *openRouterProvider) Provider() string         { return "openrouter" }
func (p *openRouterProvider) SetSystemPrompt(s string) { p.systemPrompt = s }

// shortModel returns the model name after the last slash (e.g. "deepseek-v4-flash:free").
func shortModel(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}
