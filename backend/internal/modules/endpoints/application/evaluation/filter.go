package evaluation

import (
	"strings"
	"unicode"
)

// Filter rules are provider expressions such as
//
//	(device.platform -eq "windows") and (device.manufacturer -startsWith "Dell" or device.model -in ["A", "B"])
//
// Only a small deterministic subset is evaluated: the properties platform, ownership (alias
// deviceOwnership), manufacturer, model and osVersion with the operators -eq, -ne, -startsWith,
// -contains and -in, combined with and/or and parentheses. Anything else makes the whole rule
// unsupported; it is never guessed. Comparison is case-insensitive.

// Tri is a three-valued truth.
type Tri string

const (
	Yes     Tri = "yes"
	No      Tri = "no"
	Unknown Tri = "unknown"
)

func not(t Tri) Tri {
	switch t {
	case Yes:
		return No
	case No:
		return Yes
	}
	return Unknown
}

func and(a, b Tri) Tri {
	if a == No || b == No {
		return No
	}
	if a == Yes && b == Yes {
		return Yes
	}
	return Unknown
}

func or(a, b Tri) Tri {
	if a == Yes || b == Yes {
		return Yes
	}
	if a == No && b == No {
		return No
	}
	return Unknown
}

const maxRuleLength = 2000

type node interface{ eval(Device) Tri }

type termNode struct {
	prop   string
	op     string
	values []string
}

type binNode struct {
	isAnd       bool
	left, right node
}

func (n binNode) eval(d Device) Tri {
	if n.isAnd {
		return and(n.left.eval(d), n.right.eval(d))
	}
	return or(n.left.eval(d), n.right.eval(d))
}

var properties = map[string]func(Device) string{
	"device.platform":        func(d Device) string { return d.Platform },
	"device.ownership":       func(d Device) string { return d.Ownership },
	"device.deviceownership": func(d Device) string { return d.Ownership },
	"device.manufacturer":    func(d Device) string { return d.Manufacturer },
	"device.model":           func(d Device) string { return d.Model },
	"device.osversion":       func(d Device) string { return d.OSVersion },
}

func (t termNode) eval(d Device) Tri {
	have := properties[t.prop](d)
	if have == "" || strings.EqualFold(have, "unknown") && (t.prop == "device.ownership" || t.prop == "device.deviceownership") {
		return Unknown
	}
	h := strings.ToLower(have)
	var r bool
	switch t.op {
	case "-eq":
		r = h == strings.ToLower(t.values[0])
	case "-ne":
		r = h != strings.ToLower(t.values[0])
	case "-startswith":
		r = strings.HasPrefix(h, strings.ToLower(t.values[0]))
	case "-contains":
		r = strings.Contains(h, strings.ToLower(t.values[0]))
	case "-in":
		for _, v := range t.values {
			if h == strings.ToLower(v) {
				r = true
				break
			}
		}
	}
	if r {
		return Yes
	}
	return No
}

// ParseFilter parses a rule. ok is false when the rule is empty, too long or uses anything outside the
// supported subset.
func parseFilter(rule string) (node, bool) {
	if len(rule) > maxRuleLength || strings.TrimSpace(rule) == "" {
		return nil, false
	}
	toks, ok := tokenize(rule)
	if !ok {
		return nil, false
	}
	p := &parser{toks: toks}
	n, ok := p.expr(0)
	if !ok || p.pos != len(p.toks) {
		return nil, false
	}
	return n, true
}

// EvalFilter evaluates a rule for a device. supported is false when the rule is outside the subset.
func EvalFilter(rule string, d Device) (result Tri, supported bool) {
	n, ok := parseFilter(rule)
	if !ok {
		return Unknown, false
	}
	return n.eval(d), true
}

type token struct {
	text   string
	quoted bool
}

func tokenize(s string) ([]token, bool) {
	var out []token
	rs := []rune(s)
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case r == '(' || r == ')' || r == '[' || r == ']' || r == ',':
			out = append(out, token{text: string(r)})
			i++
		case r == '"':
			var b strings.Builder
			i++
			closed := false
			for i < len(rs) {
				if rs[i] == '\\' && i+1 < len(rs) {
					b.WriteRune(rs[i+1])
					i += 2
					continue
				}
				if rs[i] == '"' {
					closed = true
					i++
					break
				}
				b.WriteRune(rs[i])
				i++
			}
			if !closed {
				return nil, false
			}
			out = append(out, token{text: b.String(), quoted: true})
		default:
			start := i
			for i < len(rs) && !unicode.IsSpace(rs[i]) && !strings.ContainsRune("()[],\"", rs[i]) {
				i++
			}
			out = append(out, token{text: string(rs[start:i])})
		}
		if len(out) > 400 {
			return nil, false
		}
	}
	return out, true
}

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() (token, bool) {
	if p.pos >= len(p.toks) {
		return token{}, false
	}
	return p.toks[p.pos], true
}

func (p *parser) isWord(w string) bool {
	t, ok := p.peek()
	return ok && !t.quoted && strings.EqualFold(t.text, w)
}

// expr := and ("or" and)* ; and := unary ("and" unary)*.
func (p *parser) expr(depth int) (node, bool) {
	if depth > 20 {
		return nil, false
	}
	left, ok := p.andExpr(depth)
	if !ok {
		return nil, false
	}
	for p.isWord("or") {
		p.pos++
		right, ok := p.andExpr(depth)
		if !ok {
			return nil, false
		}
		left = binNode{isAnd: false, left: left, right: right}
	}
	return left, true
}

func (p *parser) andExpr(depth int) (node, bool) {
	left, ok := p.unary(depth)
	if !ok {
		return nil, false
	}
	for p.isWord("and") {
		p.pos++
		right, ok := p.unary(depth)
		if !ok {
			return nil, false
		}
		left = binNode{isAnd: true, left: left, right: right}
	}
	return left, true
}

func (p *parser) unary(depth int) (node, bool) {
	t, ok := p.peek()
	if !ok {
		return nil, false
	}
	if !t.quoted && t.text == "(" {
		p.pos++
		n, ok := p.expr(depth + 1)
		if !ok {
			return nil, false
		}
		if c, ok := p.peek(); !ok || c.quoted || c.text != ")" {
			return nil, false
		}
		p.pos++
		return n, true
	}
	return p.term()
}

func (p *parser) term() (node, bool) {
	pt, ok := p.peek()
	if !ok || pt.quoted {
		return nil, false
	}
	prop := strings.ToLower(pt.text)
	if _, known := properties[prop]; !known {
		return nil, false
	}
	p.pos++
	ot, ok := p.peek()
	if !ok || ot.quoted {
		return nil, false
	}
	op := strings.ToLower(ot.text)
	p.pos++
	switch op {
	case "-eq", "-ne", "-startswith", "-contains":
		v, ok := p.peek()
		if !ok || !v.quoted {
			return nil, false
		}
		p.pos++
		return termNode{prop: prop, op: op, values: []string{v.text}}, true
	case "-in":
		if o, ok := p.peek(); !ok || o.quoted || o.text != "[" {
			return nil, false
		}
		p.pos++
		var vals []string
		for {
			v, ok := p.peek()
			if !ok || !v.quoted {
				return nil, false
			}
			vals = append(vals, v.text)
			p.pos++
			s, ok := p.peek()
			if !ok || s.quoted {
				return nil, false
			}
			p.pos++
			if s.text == "]" {
				break
			}
			if s.text != "," {
				return nil, false
			}
		}
		return termNode{prop: prop, op: op, values: vals}, true
	}
	return nil, false
}
