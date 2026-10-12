package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func okTool() Tool {
	return Tool{
		Name: "tickets.demo", Description: "demo", Permission: "tickets.view", Risk: RiskRead,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["ticketId"],"properties":{"ticketId":{"type":"string","format":"uuid","maxLength":36,"x-audit":"id"},"n":{"type":"integer","minimum":1,"maximum":5}}}`),
		Target:      &Target{Type: "ticket", Arg: "ticketId"},
		Output:      []Field{{Path: "title", Class: ClassBusinessRecord, MaxLen: 10}, {Path: "items[].name", Class: ClassPersonalContact}},
		Handler:     func(context.Context, Caller, json.RawMessage) (any, error) { return nil, nil },
	}
}

func TestRegistrySelfCheckRejects(t *testing.T) {
	mut := map[string]func(*Tool){
		"bad name":           func(t *Tool) { t.Name = "Tickets" },
		"unknown permission": func(t *Tool) { t.Permission = "tickets.nope" },
		"write risk":         func(t *Tool) { t.Risk = RiskWrite },
		"high impact":        func(t *Tool) { t.Risk = RiskHighImpact },
		"no handler":         func(t *Tool) { t.Handler = nil },
		"no output":          func(t *Tool) { t.Output = nil },
		"unknown class":      func(t *Tool) { t.Output = []Field{{Path: "a", Class: "secrets"}} },
		"secret field":       func(t *Tool) { t.Output = []Field{{Path: "apiToken", Class: ClassBusinessRecord}} },
		"password field":     func(t *Tool) { t.Output = []Field{{Path: "user.Password", Class: ClassBusinessRecord}} },
		"audit field":        func(t *Tool) { t.Output = []Field{{Path: "auditTrail[].id", Class: ClassBusinessRecord}} },
		"presence field":     func(t *Tool) { t.Output = []Field{{Path: "presenceState", Class: ClassBusinessRecord}} },
		"remote access":      func(t *Tool) { t.Output = []Field{{Path: "remoteAccessPeer", Class: ClassDeviceContext}} },
		"duplicate field": func(t *Tool) {
			t.Output = []Field{{Path: "a", Class: ClassBusinessRecord}, {Path: "a", Class: ClassBusinessRecord}}
		},
		"open schema": func(t *Tool) { t.InputSchema = json.RawMessage(`{"type":"object","properties":{}}`) },
		"unbounded string": func(t *Tool) {
			t.InputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"q":{"type":"string"}}}`)
		},
		"target not uuid": func(t *Tool) { t.Target = &Target{Type: "ticket", Arg: "n"} },
	}
	for name, m := range mut {
		tool := okTool()
		m(&tool)
		if err := NewRegistry().Register(tool); err == nil {
			t.Errorf("%s: tool was accepted", name)
		}
	}
	r := NewRegistry()
	if err := r.Register(okTool()); err != nil {
		t.Fatalf("valid tool rejected: %v", err)
	}
	if err := r.Register(okTool()); err == nil {
		t.Error("duplicate registration accepted")
	}
}

func okWriteTool() Tool {
	t := okTool()
	t.Name, t.Risk, t.Output, t.Handler = "tickets.demo_write", RiskWrite, nil, nil
	t.Write = &WriteSpec{Effect: EffectInternal,
		Prepare: func(context.Context, Caller, json.RawMessage) (Preparation, error) { return Preparation{}, nil },
		Execute: func(context.Context, Caller, json.RawMessage, Execution) (ExecutionResult, error) { return ExecutionResult{}, nil }}
	return t
}

// Write tools only create proposals; high_impact operations are never registrable (ADR-0029 decision 4).
func TestRegistryWriteAndHighImpactRules(t *testing.T) {
	mut := map[string]func(*Tool){
		"write with handler": func(t *Tool) {
			t.Handler = func(context.Context, Caller, json.RawMessage) (any, error) { return nil, nil }
		},
		"write without contract": func(t *Tool) { t.Write = nil },
		"write without execute":  func(t *Tool) { w := *t.Write; w.Execute = nil; t.Write = &w },
		"write without prepare":  func(t *Tool) { w := *t.Write; w.Prepare = nil; t.Write = &w },
		"outbound effect":        func(t *Tool) { w := *t.Write; w.Effect = "outbound"; t.Write = &w },
		"write without target":   func(t *Tool) { t.Target = nil },
		"write with output":      func(t *Tool) { t.Output = []Field{{Path: "title", Class: ClassBusinessRecord}} },
		"high impact":            func(t *Tool) { t.Risk = RiskHighImpact },
		"high impact no handler": func(t *Tool) { t.Risk, t.Write = RiskHighImpact, nil },
	}
	for name, m := range mut {
		tool := okWriteTool()
		m(&tool)
		if err := NewRegistry().Register(tool); err == nil {
			t.Errorf("%s: tool was accepted", name)
		}
	}
	read := okTool()
	read.Write = okWriteTool().Write
	if err := NewRegistry().Register(read); err == nil {
		t.Error("read tool with a write contract accepted")
	}
	r := NewRegistry()
	if err := r.RegisterAll(okTool(), okWriteTool()); err != nil {
		t.Fatalf("valid tools rejected: %v", err)
	}
	c := Caller{Permissions: map[string]struct{}{"tickets.view": {}}}
	p := ProviderRecord{AllowedDataClasses: DataClasses}
	if _, ok := r.Offered(c, p)["tickets.demo_write"]; !ok {
		t.Error("write tool not offered to the assistant")
	}
	if _, ok := r.OfferedRead(c, p)["tickets.demo_write"]; ok {
		t.Error("write tool offered on the read-only (MCP) path")
	}
	if _, ok := r.OfferedRead(c, p)["tickets.demo"]; !ok {
		t.Error("read tool missing on the read-only path")
	}
}

func TestOfferedFiltersByPermissionAndProviderClasses(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(okTool()); err != nil { // needs business_record and personal_contact
		t.Fatal(err)
	}
	staff := Caller{UserID: "u", Permissions: map[string]struct{}{"tickets.view": {}}}
	plain := Caller{UserID: "u"}
	all := ProviderRecord{AllowedDataClasses: []DataClass{ClassBusinessRecord, ClassPersonalContact}}
	partial := ProviderRecord{AllowedDataClasses: []DataClass{ClassBusinessRecord}}
	if len(r.Offered(staff, all)) != 1 {
		t.Error("eligible tool not offered")
	}
	if len(r.Offered(plain, all)) != 0 {
		t.Error("tool offered without the permission")
	}
	if len(r.Offered(staff, partial)) != 0 {
		t.Error("tool offered although the provider is not allowed every class it returns")
	}
	if len(r.Offered(staff, ProviderRecord{})) != 0 {
		t.Error("deny by default: a provider with no allowed class gets no tool")
	}
}

func TestInputValidation(t *testing.T) {
	sch, err := compileSchema(okTool().InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	id := "0a7e3a52-1111-4222-8333-444455556666"
	good := []string{`{"ticketId":"` + id + `"}`, `{"ticketId":"` + id + `","n":5}`}
	bad := []string{``, `[]`, `"x"`, `{}`, `{"ticketId":"nope"}`, `{"ticketId":"` + id + `","n":6}`, `{"ticketId":"` + id + `","n":1.5}`, `{"ticketId":"` + id + `","userId":"x"}`,
		`{"ticketId":"` + id + `"} {"a":1}`, `{"ticketId":5}`, `{"ticketId":"` + id + `","n":"3"}`, `{"ticketId":null}`}
	for _, g := range good {
		if _, err := sch.validate(json.RawMessage(g)); err != nil {
			t.Errorf("%s: %v", g, err)
		}
	}
	for _, b := range bad {
		if _, err := sch.validate(json.RawMessage(b)); err == nil {
			t.Errorf("%q accepted", b)
		}
	}
	args, _ := sch.validate(json.RawMessage(`{"ticketId":"` + strings.ToUpper(id) + `","n":2}`))
	a := sch.auditInput(args)
	if a["ticketId"] != id || a["n"] == nil {
		t.Errorf("audit input: %v", a)
	}
}

func TestFilterOutputIsAnAllowlist(t *testing.T) {
	allow := outputAllowlist{"title": {Path: "title", Class: ClassBusinessRecord, MaxLen: 5}, "items[].name": {Path: "items[].name", Class: ClassPersonalContact}}
	type item struct {
		Name string `json:"name"`
	}
	out, classes, counts, err := filterOutput(struct {
		Title string `json:"title"`
		Items []item `json:"items"`
	}{"abcdefghij", []item{{"a"}, {"b"}}}, allow, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"items":[{"name":"a"},{"name":"b"}],"title":"abcd…"}` || len(classes) != 2 || counts["items"] != 2 {
		t.Errorf("out=%s classes=%v counts=%v", out, classes, counts)
	}
	// A field that is not declared is a bug in the tool and is refused, not silently passed on.
	if _, _, _, err := filterOutput(struct {
		Title    string `json:"title"`
		Internal string `json:"internalNote"`
	}{"x", "leak"}, allow, 1000); err == nil {
		t.Error("undeclared field passed the egress filter")
	}
	// Nested undeclared field.
	if _, _, _, err := filterOutput(map[string]any{"items": []map[string]any{{"name": "a", "email": "e"}}}, allow, 1000); err == nil {
		t.Error("undeclared nested field passed the egress filter")
	}
	if _, _, _, err := filterOutput(struct {
		Title string `json:"title"`
	}{"x"}, allow, 5); err != errResultTooLarge {
		t.Errorf("size cap: %v", err)
	}
}
