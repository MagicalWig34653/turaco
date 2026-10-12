package ai

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
)

// The Turaco MCP server is a transport over the same tool registry (ADR-0029 decision 7, design M4 and M8 to M10):
// the same offered set, schema validation, permission re-check, output allowlist, egress class check, caps and
// audit as the assistant, with the MCP client in the place of the AI Provider and via=mcp.

// MCPTool is the MCP description of an offered tool.
type MCPTool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// MCPTools lists the tools the principal's client may call for this User, sorted by name.
func (s *Service) MCPTools(ctx context.Context, p MCPPrincipal) ([]MCPTool, error) {
	if _, err := s.MCPOpen(ctx); err != nil {
		return nil, err
	}
	offered := s.offeredTo(p.Caller, p.Client)
	out := make([]MCPTool, 0, len(offered))
	for _, t := range offered {
		out = append(out, MCPTool{Name: t.tool.Name, Description: t.tool.Description, InputSchema: t.tool.InputSchema})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// mcpCallReserve is the worst-case token reservation of one tools/call: the maximum result size at one token per two
// bytes plus the envelope.
const mcpCallReserve = maxToolResultBytes/2 + 256

// ErrMCPUnknownTool is a tools/call for a tool that is not offered to this client and User.
var ErrMCPUnknownTool = errors.New("ai: unknown mcp tool")

// MCPCallResult is the outcome of one tools/call. Content is the tool result as inert data inside the
// <untrusted_data> envelope, or a fixed-text error; IsError marks refusals and failures.
type MCPCallResult struct {
	Content string
	IsError bool
	Outcome string
}

// MCPCall runs one tool call for an authenticated MCP principal. It returns ErrRateLimited or ErrBudgetExceeded when
// a cap refuses the call, ErrMCPUnknownTool for a tool that is not offered, and ErrForbidden when ai.use was revoked.
func (s *Service) MCPCall(ctx context.Context, p MCPPrincipal, name string, args json.RawMessage) (MCPCallResult, error) {
	st, err := s.MCPOpen(ctx)
	if err != nil {
		return MCPCallResult{}, err
	}
	// The same Caller construction as the assistant: permissions are reloaded for this call.
	c, err := s.freshCaller(ctx, p.Caller)
	if err != nil {
		return MCPCallResult{}, err
	}
	if s.cfg.Permissions == nil {
		return MCPCallResult{}, ErrForbidden
	}
	if _, err := s.gate(ctx, c); err != nil {
		return MCPCallResult{}, err
	}
	offered := s.offeredTo(c, p.Client)
	ex := toolExec{via: "mcp", targetType: "ai_mcp_grant", targetID: p.GrantID, clientID: p.ClientID, allows: p.Client.Allows}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	// Caps (design M10): one request and the worst-case result size, reserved atomically like a provider call.
	resv, err := s.store.Reserve(ctx, s.now(), c.TenantID, c.UserID, int64(mcpCallReserve), true, st.limits())
	if err != nil {
		if errors.Is(err, ErrRateLimited) || errors.Is(err, ErrBudgetExceeded) {
			code := "rate_limited"
			if errors.Is(err, ErrBudgetExceeded) {
				code = "budget_exceeded"
			}
			if aerr := s.record(ctx, c, "mcp", "ai.turn.denied", "ai_mcp_grant", p.GrantID, map[string]any{"reason": code, "clientId": p.ClientID}); aerr != nil {
				return MCPCallResult{}, aerr
			}
		}
		return MCPCallResult{}, err
	}
	if _, ok := offered[name]; !ok {
		out := toolOutcome{outcome: "unknown_tool", toolName: safeToolName(name)}
		if err := s.auditTool(ctx, c, ex, name, nil, out, 0); err != nil {
			_, _ = s.store.Settle(ctx, resv, 0, 0, 0)
			return MCPCallResult{}, err
		}
		if _, err := s.store.Settle(ctx, resv, 0, 0, 0); err != nil {
			return MCPCallResult{}, err
		}
		return MCPCallResult{}, ErrMCPUnknownTool
	}
	out, err := s.execTool(WithAllowedClasses(ctx, p.Client.AllowedDataClasses), c, ex, offered, ToolCall{ID: "mcp", Name: name, Arguments: args})
	if err != nil {
		_, _ = s.store.Settle(ctx, resv, 0, 0, 0)
		return MCPCallResult{}, err
	}
	if _, err := s.store.Settle(ctx, resv, 0, int64(len(out.content)/2+1), 0); err != nil {
		return MCPCallResult{}, err
	}
	return MCPCallResult{Content: out.content, IsError: out.outcome != "ok", Outcome: out.outcome}, nil
}
