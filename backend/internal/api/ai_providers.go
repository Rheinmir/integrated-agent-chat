package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Rheinmir/integrated-agent-chat/internal/mcp"
)

// ---------------------------------------------------------------------------
// Anthropic provider
// ---------------------------------------------------------------------------

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicProvider struct {
	model     string
	apiKey    string
	sysPrompt string
	messages  []anthropicMessage
}

func newAnthropicProvider(model, apiKey string) *anthropicProvider {
	return &anthropicProvider{model: model, apiKey: apiKey}
}

func (p *anthropicProvider) ModelID() string            { return p.model }
func (p *anthropicProvider) Provider() string           { return "anthropic" }
func (p *anthropicProvider) SetSystemPrompt(s string)   { p.sysPrompt = s }

func (p *anthropicProvider) initMessages(systemPrompt, userMsg string) {
	p.sysPrompt = systemPrompt
	p.messages = []anthropicMessage{{Role: "user", Content: userMsg}}
}

func (p *anthropicProvider) appendAssistant(text string, calls []toolCall) {
	var content []map[string]any
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
	p.messages = append(p.messages, anthropicMessage{Role: "assistant", Content: content})
}

func (p *anthropicProvider) appendToolResults(results []toolResult) {
	var content []map[string]any
	for _, r := range results {
		outputBytes, _ := json.Marshal(r.Output)
		block := map[string]any{
			"type":        "tool_result",
			"tool_use_id": r.ID,
			"content":     string(outputBytes),
		}
		if r.IsErr {
			block["is_error"] = true
		}
		content = append(content, block)
	}
	p.messages = append(p.messages, anthropicMessage{Role: "user", Content: content})
}

func (p *anthropicProvider) call(ctx context.Context, tools []mcp.Tool) (
	text string, calls []toolCall, tokIn, tokOut int, done bool, err error,
) {
	var toolDefs []map[string]any
	for _, t := range tools {
		toolDefs = append(toolDefs, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.InputSchema, // Anthropic: input_schema, NOT parameters
		})
	}

	body := map[string]any{
		"model":      p.model,
		"max_tokens": 4096,
		"system":     p.sysPrompt,
		"messages":   p.messages,
	}
	if len(toolDefs) > 0 {
		body["tools"] = toolDefs
	}

	respBytes, err := doJSON(ctx, "POST",
		"https://api.anthropic.com/v1/messages",
		map[string]string{
			"x-api-key":         p.apiKey,
			"anthropic-version": "2023-06-01",
			"content-type":      "application/json",
		},
		body,
	)
	if err != nil {
		return "", nil, 0, 0, false, err
	}

	var resp struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return "", nil, 0, 0, false, fmt.Errorf("anthropic unmarshal: %w", err)
	}
	if resp.Error != nil {
		return "", nil, 0, 0, false, fmt.Errorf("anthropic: %s", resp.Error.Message)
	}

	tokIn = resp.Usage.InputTokens
	tokOut = resp.Usage.OutputTokens

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			text += block.Text
		case "tool_use":
			calls = append(calls, toolCall{ID: block.ID, Name: block.Name, Input: block.Input})
		}
	}

	done = resp.StopReason == "end_turn" || resp.StopReason == "stop_sequence"
	return text, calls, tokIn, tokOut, done, nil
}

// ---------------------------------------------------------------------------
// Gemini provider
// ---------------------------------------------------------------------------

type geminiContent struct {
	Role  string           `json:"role,omitempty"`
	Parts []map[string]any `json:"parts"`
}

type geminiProvider struct {
	model     string
	apiKey    string
	sysPrompt string
	contents  []geminiContent
}

func newGeminiProvider(model, apiKey string) *geminiProvider {
	return &geminiProvider{model: model, apiKey: apiKey}
}

func (p *geminiProvider) ModelID() string          { return p.model }
func (p *geminiProvider) Provider() string         { return "gemini" }
func (p *geminiProvider) SetSystemPrompt(s string) { p.sysPrompt = s }

func (p *geminiProvider) initMessages(systemPrompt, userMsg string) {
	p.sysPrompt = systemPrompt
	p.contents = []geminiContent{
		{Role: "user", Parts: []map[string]any{{"text": userMsg}}},
	}
}

func (p *geminiProvider) appendAssistant(text string, calls []toolCall) {
	var parts []map[string]any
	if text != "" {
		parts = append(parts, map[string]any{"text": text})
	}
	for _, c := range calls {
		parts = append(parts, map[string]any{
			"functionCall": map[string]any{"name": c.Name, "args": c.Input},
		})
	}
	p.contents = append(p.contents, geminiContent{Role: "model", Parts: parts})
}

func (p *geminiProvider) appendToolResults(results []toolResult) {
	var parts []map[string]any
	for _, r := range results {
		var response map[string]any
		if r.IsErr {
			response = map[string]any{"error": fmt.Sprintf("%v", r.Output)}
		} else {
			switch v := r.Output.(type) {
			case map[string]any:
				response = v
			default:
				response = map[string]any{"result": v}
			}
		}
		parts = append(parts, map[string]any{
			"functionResponse": map[string]any{"name": r.Name, "response": response},
		})
	}
	p.contents = append(p.contents, geminiContent{Role: "user", Parts: parts})
}

func (p *geminiProvider) call(ctx context.Context, tools []mcp.Tool) (
	text string, calls []toolCall, tokIn, tokOut int, done bool, err error,
) {
	var funcDecls []map[string]any
	for _, t := range tools {
		funcDecls = append(funcDecls, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.InputSchema, // Gemini: parameters under functionDeclarations
		})
	}

	body := map[string]any{
		"contents": p.contents,
		"systemInstruction": map[string]any{
			"parts": []map[string]any{{"text": p.sysPrompt}},
		},
		"generationConfig": map[string]any{"maxOutputTokens": 4096},
	}
	if len(funcDecls) > 0 {
		body["tools"] = []map[string]any{{"functionDeclarations": funcDecls}}
	}

	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		p.model, p.apiKey,
	)
	respBytes, err := doJSON(ctx, "POST", url, map[string]string{"content-type": "application/json"}, body)
	if err != nil {
		return "", nil, 0, 0, false, err
	}

	var resp struct {
		Candidates []struct {
			Content      geminiContent `json:"content"`
			FinishReason string        `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
		Error *struct{ Message string `json:"message"` } `json:"error"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return "", nil, 0, 0, false, fmt.Errorf("gemini unmarshal: %w", err)
	}
	if resp.Error != nil {
		return "", nil, 0, 0, false, fmt.Errorf("gemini: %s", resp.Error.Message)
	}
	if len(resp.Candidates) == 0 {
		return "", nil, 0, 0, true, nil
	}

	tokIn = resp.UsageMetadata.PromptTokenCount
	tokOut = resp.UsageMetadata.CandidatesTokenCount
	cand := resp.Candidates[0]

	for _, part := range cand.Content.Parts {
		if t, ok := part["text"].(string); ok {
			text += t
		}
		if fc, ok := part["functionCall"].(map[string]any); ok {
			name, _ := fc["name"].(string)
			args, _ := fc["args"].(map[string]any)
			if args == nil {
				args = map[string]any{}
			}
			calls = append(calls, toolCall{
				ID:    fmt.Sprintf("gemini-%s-%d", name, len(calls)),
				Name:  name,
				Input: args,
			})
		}
	}

	done = cand.FinishReason == "STOP" || cand.FinishReason == "MAX_TOKENS"
	return text, calls, tokIn, tokOut, done, nil
}

// ---------------------------------------------------------------------------
// OpenRouter provider (OpenAI-compatible)
// ---------------------------------------------------------------------------

type orToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON string — must Unmarshal
	} `json:"function"`
}

type openRouterMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	ToolCalls  []orToolCall `json:"tool_calls,omitempty"`
	Name       string       `json:"name,omitempty"`
}

type openRouterProvider struct {
	model     string
	apiKey    string
	sysPrompt string
	messages  []openRouterMessage
}

func newOpenRouterProvider(model, apiKey string) *openRouterProvider {
	return &openRouterProvider{model: model, apiKey: apiKey}
}

func (p *openRouterProvider) ModelID() string          { return p.model }
func (p *openRouterProvider) Provider() string         { return "openrouter" }
func (p *openRouterProvider) SetSystemPrompt(s string) { p.sysPrompt = s }

func (p *openRouterProvider) initMessages(systemPrompt, userMsg string) {
	p.sysPrompt = systemPrompt
	p.messages = []openRouterMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMsg},
	}
}

func (p *openRouterProvider) appendAssistant(text string, calls []toolCall) {
	msg := openRouterMessage{Role: "assistant", Content: text}
	for _, c := range calls {
		argsBytes, _ := json.Marshal(c.Input)
		msg.ToolCalls = append(msg.ToolCalls, orToolCall{
			ID:   c.ID,
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: c.Name, Arguments: string(argsBytes)},
		})
	}
	p.messages = append(p.messages, msg)
}

func (p *openRouterProvider) appendToolResults(results []toolResult) {
	for _, r := range results {
		outputBytes, _ := json.Marshal(r.Output)
		p.messages = append(p.messages, openRouterMessage{
			Role:       "tool",
			Content:    string(outputBytes),
			ToolCallID: r.ID,
			Name:       r.Name,
		})
	}
}

func (p *openRouterProvider) call(ctx context.Context, tools []mcp.Tool) (
	text string, calls []toolCall, tokIn, tokOut int, done bool, err error,
) {
	var toolDefs []map[string]any
	for _, t := range tools {
		toolDefs = append(toolDefs, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.InputSchema,
			},
		})
	}

	body := map[string]any{
		"model":      p.model,
		"max_tokens": 4096,
		"messages":   p.messages,
	}
	if len(toolDefs) > 0 {
		body["tools"] = toolDefs
	}

	respBytes, err := doJSON(ctx, "POST",
		"https://openrouter.ai/api/v1/chat/completions",
		map[string]string{
			"Authorization": "Bearer " + p.apiKey,
			"content-type":  "application/json",
		},
		body,
	)
	if err != nil {
		return "", nil, 0, 0, false, err
	}

	var resp struct {
		Choices []struct {
			Message struct {
				Content   string       `json:"content"`
				ToolCalls []orToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct{ Message string `json:"message"` } `json:"error"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return "", nil, 0, 0, false, fmt.Errorf("openrouter unmarshal: %w", err)
	}
	if resp.Error != nil {
		return "", nil, 0, 0, false, fmt.Errorf("openrouter: %s", resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return "", nil, 0, 0, true, nil
	}

	tokIn = resp.Usage.PromptTokens
	tokOut = resp.Usage.CompletionTokens
	choice := resp.Choices[0]
	text = choice.Message.Content

	for _, tc := range choice.Message.ToolCalls {
		var input map[string]any
		// Arguments is a JSON string — always unmarshal
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
			input = map[string]any{"raw": tc.Function.Arguments}
		}
		calls = append(calls, toolCall{ID: tc.ID, Name: tc.Function.Name, Input: input})
	}

	done = choice.FinishReason == "stop" || choice.FinishReason == "length"
	return text, calls, tokIn, tokOut, done, nil
}

// ---------------------------------------------------------------------------
// Shared HTTP helper
// ---------------------------------------------------------------------------

func doJSON(ctx context.Context, method, url string, headers map[string]string, body any) ([]byte, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("doJSON marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("doJSON new request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("doJSON do: %w", err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("doJSON read: %w", err)
	}
	if resp.StatusCode >= 400 {
		s := string(respBytes)
		if len(s) > 200 {
			s = s[:200] + "..."
		}
		return respBytes, fmt.Errorf("HTTP %d: %s", resp.StatusCode, s)
	}
	return respBytes, nil
}
