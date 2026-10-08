// Package openaicompat is the AI Provider adapter for OpenAI-compatible chat-completions APIs with tool calling,
// such as a local Ollama (F12 decision A4). It only translates wire formats; vendor types never leave this
// package. All traffic goes through the validating transport of platform/ai/safehttp (A14).
package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai/safehttp"
)

// Config configures one provider. APIKey is empty for a local Ollama.
type Config struct {
	Endpoint string
	Model    string
	APIKey   string
	Local    bool
	// Client overrides the transport (tests); production always builds the safehttp client from Endpoint and Local.
	Client *safehttp.Client
}

// Provider implements ai.Provider.
type Provider struct {
	cfg    Config
	client *safehttp.Client
}

// New builds the adapter and its pinned transport.
func New(cfg Config) (*Provider, error) {
	c := cfg.Client
	if c == nil {
		var err error
		if c, err = safehttp.New(safehttp.Policy{Endpoint: cfg.Endpoint, Local: cfg.Local, MaxResponseBytes: 2 << 20}); err != nil {
			return nil, err
		}
	}
	return &Provider{cfg: cfg, client: c}, nil
}

func (p *Provider) Capabilities() ai.Capabilities {
	return ai.Capabilities{ToolCalling: true, Local: p.cfg.Local, MaxContext: 32768}
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type wireRequest struct {
	Model     string        `json:"model"`
	Messages  []wireMessage `json:"messages"`
	Tools     []wireTool    `json:"tools,omitempty"`
	MaxTokens int           `json:"max_tokens,omitempty"`
	Stream    bool          `json:"stream"`
}

type wireResponse struct {
	Choices []struct {
		Message wireMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Chat sends one chat-completions request. Errors carry the HTTP status or transport error, never content.
func (p *Provider) Chat(ctx context.Context, req ai.ChatRequest) (ai.ChatResponse, error) {
	body := wireRequest{Model: p.cfg.Model, MaxTokens: req.MaxOutputTokens}
	if req.System != "" {
		body.Messages = append(body.Messages, wireMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		wm := wireMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name}
		for _, tc := range m.ToolCalls {
			var w wireToolCall
			w.ID, w.Type = tc.ID, "function"
			w.Function.Name, w.Function.Arguments = tc.Name, string(tc.Arguments)
			wm.ToolCalls = append(wm.ToolCalls, w)
		}
		body.Messages = append(body.Messages, wm)
	}
	for _, t := range req.Tools {
		var w wireTool
		w.Type = "function"
		w.Function.Name, w.Function.Description, w.Function.Parameters = t.Name, t.Description, t.Parameters
		body.Tools = append(body.Tools, w)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return ai.ChatResponse{}, fmt.Errorf("%w: encode request", ai.ErrProviderFailed)
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, p.client.URL("chat/completions"), bytes.NewReader(raw))
	if err != nil {
		return ai.ChatResponse{}, fmt.Errorf("%w: build request", ai.ErrProviderFailed)
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	if p.cfg.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	status, data, err := p.client.Do(hr)
	if err != nil {
		return ai.ChatResponse{}, fmt.Errorf("%w: %v", ai.ErrProviderFailed, err)
	}
	if status < 200 || status > 299 {
		return ai.ChatResponse{}, fmt.Errorf("%w: status %d", ai.ErrProviderFailed, status)
	}
	var wr wireResponse
	if err := json.Unmarshal(data, &wr); err != nil || len(wr.Choices) == 0 {
		return ai.ChatResponse{}, fmt.Errorf("%w: unexpected response shape", ai.ErrProviderFailed)
	}
	msg := wr.Choices[0].Message
	out := ai.ChatResponse{Content: msg.Content, TokensIn: wr.Usage.PromptTokens, TokensOut: wr.Usage.CompletionTokens}
	for _, tc := range msg.ToolCalls {
		args := strings.TrimSpace(tc.Function.Arguments)
		out.ToolCalls = append(out.ToolCalls, ai.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: json.RawMessage(args)})
	}
	return out, nil
}
