package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
)

func TestChatTranslatesWireFormatAndToolCalls(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"tickets.summarize","arguments":"{\"ticketId\":\"x\"}"}}]}}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`))
	}))
	defer srv.Close()
	p, err := New(Config{Endpoint: srv.URL + "/v1", Model: "llama", APIKey: "k-123", Local: true})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Chat(context.Background(), ai.ChatRequest{System: "sys", MaxOutputTokens: 99,
		Messages: []ai.Message{{Role: "user", Content: "hi"}, {Role: "assistant", ToolCalls: []ai.ToolCall{{ID: "c0", Name: "knowledge.search", Arguments: json.RawMessage(`{"query":"a"}`)}}},
			{Role: "tool", ToolCallID: "c0", Name: "knowledge.search", Content: "<untrusted_data>{}</untrusted_data>"}},
		Tools: []ai.ToolDef{{Name: "tickets.summarize", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer k-123" || got["model"] != "llama" || got["max_tokens"].(float64) != 99 || got["stream"] != false {
		t.Errorf("request: auth=%q body=%v", auth, got)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" || msgs[3].(map[string]any)["tool_call_id"] != "c0" {
		t.Errorf("messages: %v", msgs)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "tickets.summarize" || string(resp.ToolCalls[0].Arguments) != `{"ticketId":"x"}` || resp.TokensIn != 11 || resp.TokensOut != 7 {
		t.Errorf("response: %+v", resp)
	}
}

func TestChatErrorsCarryNoContentAndRedirectsFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "chat/completions") {
			http.Redirect(w, r, "http://169.254.169.254/latest", http.StatusTemporaryRedirect)
		}
	}))
	defer srv.Close()
	p, err := New(Config{Endpoint: srv.URL + "/v1", Model: "m", Local: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Chat(context.Background(), ai.ChatRequest{Messages: []ai.Message{{Role: "user", Content: "secret prompt"}}}); !errors.Is(err, ai.ErrProviderFailed) || strings.Contains(err.Error(), "secret prompt") {
		t.Errorf("redirect: %v", err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("provider said: secret prompt"))
	}))
	defer bad.Close()
	p, _ = New(Config{Endpoint: bad.URL, Model: "m", Local: true})
	if _, err := p.Chat(context.Background(), ai.ChatRequest{}); !errors.Is(err, ai.ErrProviderFailed) || strings.Contains(err.Error(), "secret prompt") {
		t.Errorf("status error must not echo the body: %v", err)
	}
}

func TestExternalEndpointToLoopbackIsRejectedAtConstruction(t *testing.T) {
	if _, err := New(Config{Endpoint: "https://127.0.0.1/v1", Model: "m"}); err == nil {
		t.Fatal("external provider on loopback accepted")
	}
	if _, err := New(Config{Endpoint: "http://provider.example/v1", Model: "m"}); err == nil {
		t.Fatal("plain http external provider accepted")
	}
}
