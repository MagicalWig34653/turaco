// Package fake is the scripted AI Provider for tests and demos (F12 decision A4), the same pattern as the F9/F10
// fakes. It talks to no network. Scripted replays a fixed list of steps; Deterministic follows simple rules so a
// demo installation can show tool use without a model.
package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
)

// Step produces the provider's answer to one call.
type Step func(req ai.ChatRequest) (ai.ChatResponse, error)

// Provider implements ai.Provider.
type Provider struct {
	mu    sync.Mutex
	steps []Step
	rule  bool
	reqs  []ai.ChatRequest
}

// Scripted answers call n with steps[n]; calls beyond the script fail.
func Scripted(steps ...Step) *Provider { return &Provider{steps: steps} }

// Deterministic answers by rules: after a tool result it summarizes the number of results; a message with an id and
// "ticket" or "device" calls the matching tool; "search" or "knowledge" calls knowledge.search; otherwise it echoes.
func Deterministic() *Provider { return &Provider{rule: true} }

// Text is a step that answers with text.
func Text(s string) Step {
	return func(ai.ChatRequest) (ai.ChatResponse, error) { return ai.ChatResponse{Content: s}, nil }
}

// Call is a step that requests one tool call.
func Call(id, name, args string) Step {
	return func(ai.ChatRequest) (ai.ChatResponse, error) {
		return ai.ChatResponse{ToolCalls: []ai.ToolCall{{ID: id, Name: name, Arguments: json.RawMessage(args)}}}, nil
	}
}

// Calls is a step that requests several tool calls at once.
func Calls(calls ...ai.ToolCall) Step {
	return func(ai.ChatRequest) (ai.ChatResponse, error) { return ai.ChatResponse{ToolCalls: calls}, nil }
}

// Requests returns the requests received so far (tests inspect what the model would have seen).
func (p *Provider) Requests() []ai.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ai.ChatRequest(nil), p.reqs...)
}

func (p *Provider) Capabilities() ai.Capabilities {
	return ai.Capabilities{ToolCalling: true, Local: true, MaxContext: 32768}
}

var idRE = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

func (p *Provider) Chat(ctx context.Context, req ai.ChatRequest) (ai.ChatResponse, error) {
	if err := ctx.Err(); err != nil {
		return ai.ChatResponse{}, err
	}
	p.mu.Lock()
	n := len(p.reqs)
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	var resp ai.ChatResponse
	var err error
	switch {
	case p.rule:
		resp = p.respond(req)
	case n < len(p.steps):
		resp, err = p.steps[n](req)
	default:
		err = fmt.Errorf("fake provider: no scripted step for call %d", n)
	}
	if err != nil {
		return ai.ChatResponse{}, err
	}
	resp.TokensIn, resp.TokensOut = 10+len(req.Messages), 5+len(resp.Content)/4
	return resp, nil
}

func (p *Provider) respond(req ai.ChatRequest) ai.ChatResponse {
	if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == "tool" {
		return ai.ChatResponse{Content: "I looked the data up with " + req.Messages[n-1].Name + "."}
	}
	last := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			last = req.Messages[i].Content
			break
		}
	}
	lower := strings.ToLower(last)
	has := func(tool string) bool {
		for _, t := range req.Tools {
			if t.Name == tool {
				return true
			}
		}
		return false
	}
	id := idRE.FindString(last)
	switch {
	case id != "" && strings.Contains(lower, "ticket") && has("tickets.summarize"):
		return ai.ChatResponse{ToolCalls: []ai.ToolCall{{ID: "call-1", Name: "tickets.summarize", Arguments: mustArgs("ticketId", id)}}}
	case id != "" && strings.Contains(lower, "device") && has("devices.context_summary"):
		return ai.ChatResponse{ToolCalls: []ai.ToolCall{{ID: "call-1", Name: "devices.context_summary", Arguments: mustArgs("deviceId", id)}}}
	case (strings.Contains(lower, "search") || strings.Contains(lower, "knowledge")) && has("knowledge.search"):
		q, _ := json.Marshal(strings.TrimSpace(last))
		if len(q) > 200 {
			q = q[:200]
		}
		return ai.ChatResponse{ToolCalls: []ai.ToolCall{{ID: "call-1", Name: "knowledge.search", Arguments: json.RawMessage(`{"query":` + string(q) + `}`)}}}
	}
	return ai.ChatResponse{Content: "Fake assistant: " + last}
}

func mustArgs(key, value string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{key: value})
	return raw
}
