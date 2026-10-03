package application

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	userA   = "00000000-0000-7000-8000-0000000000a1"
	teamA   = "00000000-0000-7000-8000-0000000000e1"
	prodA   = "00000000-0000-7000-8000-0000000000f1"
	prodB   = "00000000-0000-7000-8000-0000000000f2"
	prodC   = "00000000-0000-7000-8000-0000000000f3"
	catLap  = "00000000-0000-7000-8000-0000000000c1"
	catMon  = "00000000-0000-7000-8000-0000000000c2"
	userOff = "00000000-0000-7000-8000-0000000000b9"
)

func parse(t *testing.T, raw string) (Definition, error) {
	t.Helper()
	return ParseDefinition([]byte(raw))
}

func mustParse(t *testing.T, raw string) Definition {
	t.Helper()
	d, err := parse(t, raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return d
}

func TestParseDefinitionAcceptsAFullDefinitionAndNormalizes(t *testing.T) {
	d := mustParse(t, `{
		"allowRequestedFor": true,
		"fields": [
			{"key":"reason","type":"longtext","label":"  Reason  ","required":true},
			{"key":"title","type":"text","label":"Title"},
			{"key":"count","type":"number","label":"Count","min":1,"max":5},
			{"key":"urgent","type":"boolean","label":"Urgent"},
			{"key":"needBy","type":"date","label":"Needed by"},
			{"key":"color","type":"select","label":"Color","options":[{"value":"black","label":"Black"},{"value":"silver","label":"Silver"}]},
			{"key":"forUser","type":"user","label":"For"},
			{"key":"device","type":"product","label":"Device","categoryId":"`+catLap+`","required":true}
		],
		"approvals": [{"approver":"manager"},{"approverTeamId":"`+teamA+`"},{"approverUserId":"`+userA+`"}],
		"fulfillment": [{"title":" Prepare ","priority":"high","assignedTeamId":"`+teamA+`","dueAfterHours":48},{"title":"Optional","mandatory":false}]
	}`)
	if d.Fields[0].Label != "Reason" || *d.Fields[0].MaxLength != 2000 || *d.Fields[1].MaxLength != 200 {
		t.Errorf("defaults/normalization: %+v", d.Fields[:2])
	}
	if d.Fulfillment[0].Title != "Prepare" || !d.Fulfillment[0].IsMandatory() || d.Fulfillment[1].IsMandatory() {
		t.Errorf("fulfillment = %+v", d.Fulfillment)
	}
	raw, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseDefinition(raw)
	if err != nil {
		t.Fatalf("the canonical form must parse again: %v", err)
	}
	raw2, _ := again.Marshal()
	if string(raw) != string(raw2) {
		t.Error("marshalling must be stable")
	}
	if empty := mustParse(t, `{}`); empty.Fields != nil || len(mustMarshal(t, empty)) == 0 {
		t.Error("an empty definition is valid")
	}
}

func mustMarshal(t *testing.T, d Definition) []byte {
	t.Helper()
	b, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseDefinitionRejects(t *testing.T) {
	field := func(extra string) string { return `{"fields":[{"key":"a","type":"text","label":"A"` + extra + `}]}` }
	many := func(n int, tpl string) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = strings.ReplaceAll(tpl, "#", string(rune('a'+i%26))+string(rune('a'+i/26)))
		}
		return strings.Join(parts, ",")
	}
	cases := map[string]string{
		"not json":               `[`,
		"unknown property":       `{"fields":[],"script":"x"}`,
		"unknown field property": field(`,"default":"x"`),
		"trailing data":          `{} {}`,
		"bad key":                `{"fields":[{"key":"Bad","type":"text","label":"A"}]}`,
		"key with space":         `{"fields":[{"key":"a b","type":"text","label":"A"}]}`,
		"duplicate key":          `{"fields":[{"key":"a","type":"text","label":"A"},{"key":"a","type":"text","label":"B"}]}`,
		"unknown type":           `{"fields":[{"key":"a","type":"file","label":"A"}]}`,
		"empty label":            `{"fields":[{"key":"a","type":"text","label":" "}]}`,
		"override label":         `{"fields":[{"key":"a","type":"text","label":"A‮"}]}`,
		"text maxLength too big": field(`,"maxLength":1001`),
		"maxLength zero":         field(`,"maxLength":0`),
		"maxLength on number":    `{"fields":[{"key":"a","type":"number","label":"A","maxLength":5}]}`,
		"min above max":          `{"fields":[{"key":"a","type":"number","label":"A","min":5,"max":1}]}`,
		"min on text":            field(`,"min":1`),
		"select without options": `{"fields":[{"key":"a","type":"select","label":"A"}]}`,
		"options on text":        field(`,"options":[{"value":"x","label":"X"}]`),
		"duplicate option":       `{"fields":[{"key":"a","type":"select","label":"A","options":[{"value":"x","label":"X"},{"value":"x","label":"Y"}]}]}`,
		"bad option value":       `{"fields":[{"key":"a","type":"select","label":"A","options":[{"value":"X Y","label":"X"}]}]}`,
		"product without choice": `{"fields":[{"key":"a","type":"product","label":"A"}]}`,
		"product with both":      `{"fields":[{"key":"a","type":"product","label":"A","categoryId":"` + catLap + `","productIds":["` + prodA + `"]}]}`,
		"product bad id":         `{"fields":[{"key":"a","type":"product","label":"A","productIds":["x"]}]}`,
		"product duplicate":      `{"fields":[{"key":"a","type":"product","label":"A","productIds":["` + prodA + `","` + prodA + `"]}]}`,
		"category on text":       field(`,"categoryId":"` + catLap + `"`),
		"too many fields":        `{"fields":[` + many(MaxFields+1, `{"key":"f#","type":"boolean","label":"L"}`) + `]}`,
		"too many steps":         `{"approvals":[` + many(MaxApprovalSteps+1, `{"approver":"manager"}`) + `]}`,
		"step without approver":  `{"approvals":[{}]}`,
		"step with two":          `{"approvals":[{"approver":"manager","approverTeamId":"` + teamA + `"}]}`,
		"unknown approver":       `{"approvals":[{"approver":"ceo"}]}`,
		"step bad user id":       `{"approvals":[{"approverUserId":"x"}]}`,
		"too many tasks":         `{"fulfillment":[` + many(MaxFulfillment+1, `{"title":"T"}`) + `]}`,
		"task without title":     `{"fulfillment":[{"title":" "}]}`,
		"task bad priority":      `{"fulfillment":[{"title":"T","priority":"asap"}]}`,
		"task bad assignee":      `{"fulfillment":[{"title":"T","assignedUserId":"x"}]}`,
		"task due too large":     `{"fulfillment":[{"title":"T","dueAfterHours":9000}]}`,
		"task due zero":          `{"fulfillment":[{"title":"T","dueAfterHours":0}]}`,
	}
	for name, raw := range cases {
		var inv *InvalidInputError
		if _, err := ParseDefinition([]byte(raw)); !errors.As(err, &inv) {
			t.Errorf("%s: err = %v, want InvalidInputError", name, err)
		}
	}
}

type users map[string]bool

func (u users) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = u[id]
	}
	return out, nil
}

type products map[string]string // active product id -> category

func (p products) ActiveProducts(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if c, ok := p[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

var (
	activeUsers    = users{userA: true, userOff: false}
	activeProducts = products{prodA: catLap, prodB: catLap, prodC: catMon}
)

func validate(t *testing.T, d Definition, answers map[string]any) (map[string]any, []Reference, error) {
	t.Helper()
	return ValidateAnswers(context.Background(), d, answers, activeUsers, activeProducts)
}

func fieldErrors(t *testing.T, err error) map[string]string {
	t.Helper()
	var fe *FieldErrors
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want FieldErrors", err)
	}
	return fe.Errors
}

var formDef = `{"fields":[
	{"key":"reason","type":"longtext","label":"Reason","required":true,"maxLength":50},
	{"key":"title","type":"text","label":"Title","maxLength":10},
	{"key":"count","type":"number","label":"Count","min":1,"max":5},
	{"key":"urgent","type":"boolean","label":"Urgent"},
	{"key":"needBy","type":"date","label":"Needed by"},
	{"key":"color","type":"select","label":"Color","options":[{"value":"black","label":"Black"}]},
	{"key":"forUser","type":"user","label":"For"},
	{"key":"device","type":"product","label":"Device","categoryId":"` + catLap + `"},
	{"key":"extra","type":"product","label":"Extra","productIds":["` + prodA + `"]}
]}`

func TestValidAnswersAreNormalizedAndReferencesPromoted(t *testing.T) {
	d := mustParse(t, formDef)
	out, refs, err := validate(t, d, map[string]any{
		"reason": "  needs a laptop \n for work ", "title": "  Hi ", "count": float64(3), "urgent": true, "needBy": "2026-12-01",
		"color": "black", "forUser": strings.ToUpper(userA), "device": prodA, "extra": prodA,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["reason"] != "needs a laptop \n for work" || out["title"] != "Hi" || out["count"] != 3 || out["urgent"] != true || out["forUser"] != userA {
		t.Errorf("normalized = %#v", out)
	}
	if len(refs) != 3 || refs[0].FieldKey != "device" || refs[0].Type != "product" || refs[1].FieldKey != "extra" || refs[2].FieldKey != "forUser" || refs[2].Type != "user" || refs[2].ID != userA {
		t.Errorf("references = %+v", refs)
	}
}

func TestOptionalAnswersMayBeOmittedOrEmpty(t *testing.T) {
	d := mustParse(t, formDef)
	out, refs, err := validate(t, d, map[string]any{"reason": "x", "title": "", "count": nil})
	if err != nil || len(out) != 1 || len(refs) != 0 {
		t.Errorf("out=%v refs=%v err=%v", out, refs, err)
	}
}

func TestInvalidAnswersReportOneCodePerField(t *testing.T) {
	d := mustParse(t, formDef)
	cases := map[string]struct {
		answers map[string]any
		key     string
		code    string
	}{
		"missing required":       {map[string]any{}, "reason", CodeRequired},
		"blank required":         {map[string]any{"reason": "   "}, "reason", CodeRequired},
		"unknown key":            {map[string]any{"reason": "x", "hack": 1}, "hack", CodeUnknownField},
		"text too long":          {map[string]any{"reason": "x", "title": strings.Repeat("a", 11)}, "title", CodeTooLong},
		"text wrong type":        {map[string]any{"reason": "x", "title": 5.0}, "title", CodeType},
		"override characters":    {map[string]any{"reason": "ok‮"}, "reason", CodeCharacters},
		"control in text":        {map[string]any{"reason": "x", "title": "a\nb"}, "title", CodeCharacters},
		"number fraction":        {map[string]any{"reason": "x", "count": 2.5}, "count", CodeType},
		"number string":          {map[string]any{"reason": "x", "count": "3"}, "count", CodeType},
		"number below":           {map[string]any{"reason": "x", "count": 0.0}, "count", CodeRange},
		"number above":           {map[string]any{"reason": "x", "count": 6.0}, "count", CodeRange},
		"number huge":            {map[string]any{"reason": "x", "count": 1e12}, "count", CodeType},
		"boolean string":         {map[string]any{"reason": "x", "urgent": "yes"}, "urgent", CodeType},
		"bad date":               {map[string]any{"reason": "x", "needBy": "2026-02-30"}, "needBy", CodeDate},
		"date format":            {map[string]any{"reason": "x", "needBy": "01.12.2026"}, "needBy", CodeDate},
		"unknown option":         {map[string]any{"reason": "x", "color": "pink"}, "color", CodeChoice},
		"user not an id":         {map[string]any{"reason": "x", "forUser": "bob"}, "forUser", CodeType},
		"user inactive":          {map[string]any{"reason": "x", "forUser": userOff}, "forUser", CodeNotActive},
		"user unknown":           {map[string]any{"reason": "x", "forUser": "00000000-0000-7000-8000-0000000000ee"}, "forUser", CodeNotActive},
		"product other category": {map[string]any{"reason": "x", "device": prodC}, "device", CodeNotAllowed},
		"product not listed":     {map[string]any{"reason": "x", "extra": prodB}, "extra", CodeNotAllowed},
		"product unknown":        {map[string]any{"reason": "x", "device": "00000000-0000-7000-8000-0000000000ee"}, "device", CodeNotActive},
	}
	for name, c := range cases {
		_, _, err := validate(t, d, c.answers)
		got := fieldErrors(t, err)
		if got[c.key] != c.code {
			t.Errorf("%s: %v, want %s=%s", name, got, c.key, c.code)
		}
	}
	// All problems are reported together.
	_, _, err := validate(t, d, map[string]any{"title": strings.Repeat("a", 11), "count": 9.0})
	if got := fieldErrors(t, err); len(got) != 3 || got["reason"] != CodeRequired {
		t.Errorf("combined = %v", got)
	}
}

func TestProductAnswersAreCheckedAgainstActiveProductsOnly(t *testing.T) {
	d := mustParse(t, formDef)
	inactive := products{} // nothing is active
	_, _, err := ValidateAnswers(context.Background(), d, map[string]any{"reason": "x", "device": prodA}, activeUsers, inactive)
	if got := fieldErrors(t, err); got["device"] != CodeNotActive {
		t.Errorf("deactivated product: %v", got)
	}
}

func TestFieldErrorsMessageIsStable(t *testing.T) {
	fe := &FieldErrors{Errors: map[string]string{"b": CodeType, "a": CodeRequired}}
	if fe.Error() != "answers are invalid: a: required; b: invalid_type" {
		t.Errorf("message = %q", fe.Error())
	}
}

func TestAnswersAgainstAStoredDefinitionWithoutMaxLength(t *testing.T) {
	// A snapshot decoded from storage is not re-normalized; it must not crash.
	d := Definition{Fields: []Field{{Key: "a", Type: FieldText, Label: "A"}, {Key: "b", Type: FieldLongText, Label: "B"}}}
	if _, _, err := validate(t, d, map[string]any{"a": "ok", "b": "ok"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := validate(t, d, map[string]any{"a": strings.Repeat("x", maxTextDefault+1)})
	if got := fieldErrors(t, err); got["a"] != CodeTooLong {
		t.Errorf("default limit not applied: %v", got)
	}
}
