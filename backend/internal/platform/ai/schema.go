package ai

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// schemaNode is the supported JSON-schema subset for tool input: an object whose properties are strings
// (maxLength required, optional enum and format "uuid"), integers (minimum and maximum required) or booleans.
// Unknown properties are always rejected. The vocabulary is deliberately small: it is validated server-side for
// every call, because a model may ignore the schema it was given.
type schemaNode struct {
	Type                 string                 `json:"type"`
	Properties           map[string]*schemaNode `json:"properties"`
	Required             []string               `json:"required"`
	AdditionalProperties *bool                  `json:"additionalProperties"`
	MaxLength            *int                   `json:"maxLength"`
	MinLength            *int                   `json:"minLength"`
	Minimum              *int64                 `json:"minimum"`
	Maximum              *int64                 `json:"maximum"`
	Enum                 []string               `json:"enum"`
	Format               string                 `json:"format"`
	Audit                string                 `json:"x-audit"`
	Description          string                 `json:"description"`
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// compileSchema parses and checks the tool input schema (registration time).
func compileSchema(raw json.RawMessage) (*schemaNode, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var s schemaNode
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("input schema: %w", err)
	}
	if s.Type != "object" || s.AdditionalProperties == nil || *s.AdditionalProperties {
		return nil, errors.New("input schema: must be an object with additionalProperties false")
	}
	for name, p := range s.Properties {
		if p == nil {
			return nil, fmt.Errorf("input schema: property %q is empty", name)
		}
		switch p.Type {
		case "string":
			if p.MaxLength == nil || *p.MaxLength < 1 || *p.MaxLength > 2000 {
				return nil, fmt.Errorf("input schema: string property %q needs maxLength 1..2000", name)
			}
			if p.Format != "" && p.Format != "uuid" {
				return nil, fmt.Errorf("input schema: property %q has unsupported format", name)
			}
		case "integer":
			if p.Minimum == nil || p.Maximum == nil || *p.Minimum > *p.Maximum {
				return nil, fmt.Errorf("input schema: integer property %q needs minimum and maximum", name)
			}
		case "boolean":
		default:
			return nil, fmt.Errorf("input schema: property %q has unsupported type %q", name, p.Type)
		}
		if p.Audit != "" && p.Audit != "id" && p.Audit != "text" {
			return nil, fmt.Errorf("input schema: property %q has unsupported x-audit", name)
		}
		if p.Audit == "id" && p.Format != "uuid" {
			return nil, fmt.Errorf("input schema: x-audit id on %q needs format uuid", name)
		}
	}
	for _, r := range s.Required {
		if _, ok := s.Properties[r]; !ok {
			return nil, fmt.Errorf("input schema: required property %q is not defined", r)
		}
	}
	return &s, nil
}

// validate checks a model-supplied argument object against the schema and returns the decoded values.
func (s *schemaNode) validate(raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	if len(raw) > 8<<10 {
		return nil, errors.New("arguments are too large")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, errors.New("arguments are not valid JSON")
	}
	if dec.More() {
		return nil, errors.New("arguments are not a single JSON value")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("arguments must be an object")
	}
	for k := range obj {
		if _, known := s.Properties[k]; !known {
			return nil, fmt.Errorf("unknown argument %q", k)
		}
	}
	for _, r := range s.Required {
		if val, present := obj[r]; !present || val == nil {
			return nil, fmt.Errorf("argument %q is required", r)
		}
	}
	for k, val := range obj {
		p := s.Properties[k]
		if val == nil {
			delete(obj, k)
			continue
		}
		switch p.Type {
		case "string":
			str, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf("argument %q must be a string", k)
			}
			if !utf8.ValidString(str) || strings.ContainsRune(str, 0) || utf8.RuneCountInString(str) > *p.MaxLength {
				return nil, fmt.Errorf("argument %q is invalid or too long", k)
			}
			if p.MinLength != nil && utf8.RuneCountInString(str) < *p.MinLength {
				return nil, fmt.Errorf("argument %q is too short", k)
			}
			if p.Format == "uuid" && !uuidRE.MatchString(str) {
				return nil, fmt.Errorf("argument %q must be a UUID", k)
			}
			if len(p.Enum) > 0 && !slices.Contains(p.Enum, str) {
				return nil, fmt.Errorf("argument %q is not an allowed value", k)
			}
		case "integer":
			n, ok := val.(json.Number)
			if !ok {
				return nil, fmt.Errorf("argument %q must be an integer", k)
			}
			i, err := n.Int64()
			if err != nil || i < *p.Minimum || i > *p.Maximum {
				return nil, fmt.Errorf("argument %q is out of range", k)
			}
		case "boolean":
			if _, ok := val.(bool); !ok {
				return nil, fmt.Errorf("argument %q must be a boolean", k)
			}
		}
	}
	return obj, nil
}

// auditInput reduces validated arguments to what audit may store: ids, integers and a hash plus length of free text.
// Strings without an x-audit marker are dropped.
func (s *schemaNode) auditInput(args map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range args {
		p := s.Properties[k]
		switch {
		case p.Type == "integer":
			out[k] = v
		case p.Type == "boolean":
			out[k] = v
		case p.Audit == "id":
			out[k] = strings.ToLower(v.(string))
		case p.Audit == "text":
			str := v.(string)
			sum := sha256.Sum256([]byte(str))
			out[k+"Hash"] = hex.EncodeToString(sum[:8])
			out[k+"Length"] = utf8.RuneCountInString(str)
		}
	}
	return out
}

// outputAllowlist maps output paths to their field.
type outputAllowlist map[string]Field

const defaultMaxLen = 500

// filterOutput serializes a handler's DTO and enforces the field allowlist (A13): every leaf must be declared;
// strings are cut to their MaxLen. It returns the sanitized JSON, the classes actually present and the top-level
// array sizes for audit. An undeclared field is an error, not silently dropped: it is a bug in the tool.
func filterOutput(dto any, allow outputAllowlist, maxBytes int) (json.RawMessage, []DataClass, map[string]int, error) {
	raw, err := json.Marshal(dto)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal tool output: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, nil, nil, fmt.Errorf("decode tool output: %w", err)
	}
	classes := map[DataClass]bool{}
	var walkErr error
	var walk func(v any, path string) any
	walk = func(v any, path string) any {
		if walkErr != nil {
			return nil
		}
		switch t := v.(type) {
		case map[string]any:
			for k, child := range t {
				cp := k
				if path != "" {
					cp = path + "." + k
				}
				t[k] = walk(child, cp)
			}
			return t
		case []any:
			for i, child := range t {
				t[i] = walk(child, path+"[]")
			}
			return t
		case nil:
			return nil
		default:
			f, ok := allow[path]
			if !ok {
				walkErr = fmt.Errorf("tool output contains undeclared field %q", path)
				return nil
			}
			classes[f.Class] = true
			if str, isStr := t.(string); isStr {
				limit := f.MaxLen
				if limit <= 0 {
					limit = defaultMaxLen
				}
				if utf8.RuneCountInString(str) > limit {
					r := []rune(str)
					return string(r[:limit-1]) + "…"
				}
			}
			return t
		}
	}
	v = walk(v, "")
	if walkErr != nil {
		return nil, nil, nil, walkErr
	}
	counts := map[string]int{}
	if obj, ok := v.(map[string]any); ok {
		for k, child := range obj {
			if arr, isArr := child.([]any); isArr {
				counts[k] = len(arr)
			}
		}
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal sanitized output: %w", err)
	}
	if len(out) > maxBytes {
		return nil, nil, nil, errResultTooLarge
	}
	var cl []DataClass
	for c := range classes {
		cl = append(cl, c)
	}
	slices.Sort(cl)
	return out, cl, counts, nil
}

var errResultTooLarge = errors.New("tool result exceeds the size limit")
