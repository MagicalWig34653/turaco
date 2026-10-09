package views

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// canonical validates the shape of a Definition (size, columns, filter version) and returns its canonical JSON and
// hash. Field and value validity against the resource catalog is checked by dry-running the filter through the
// owning module's query endpoint (Service.validate), so the rules are exactly the module's own.
func canonical(d Definition) (Definition, []byte, string, error) {
	if len(d.Columns) > MaxColumns {
		return d, nil, "", invalid("Too many columns.")
	}
	seen := map[string]bool{}
	for _, c := range d.Columns {
		if !columnPattern.MatchString(c) || seen[c] {
			return d, nil, "", invalid("A column key is invalid or repeated.")
		}
		seen[c] = true
	}
	if d.Filter != nil {
		if d.Filter.V == 0 {
			d.Filter.V = 1
		}
		if d.Filter.V != 1 {
			return d, nil, "", invalid("Unsupported filter version.")
		}
		d.Filter.Search = strings.TrimSpace(d.Filter.Search)
		if d.Filter.Root == nil && d.Filter.Search == "" && len(d.Filter.Sort) == 0 {
			d.Filter = nil
		}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return d, nil, "", invalid("The definition cannot be encoded.")
	}
	if len(raw) > query.MaxDefinitionBytes {
		return d, nil, "", invalid("The definition is too large.")
	}
	// Strict re-decode of the canonical bytes: this is the form that is stored and later executed.
	var back Definition
	if d.Filter != nil {
		if _, err := query.DecodeFilter(mustMarshal(d.Filter)); err != nil {
			return d, nil, "", err
		}
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		return d, nil, "", invalid("The definition is not valid.")
	}
	sum := sha256.Sum256(raw)
	return back, raw, hex.EncodeToString(sum[:]), nil
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// decodeStored decodes a stored definition. Stored data was validated on write; a failure here is a data error.
func decodeStored(raw []byte) (Definition, error) {
	var d Definition
	if err := json.Unmarshal(raw, &d); err != nil {
		return Definition{}, fmt.Errorf("decode stored definition: %w", err)
	}
	return d, nil
}

// conditionCount counts the condition nodes of a filter (audit metadata; never the values).
func conditionCount(f *query.Filter) int {
	if f == nil || f.Root == nil {
		return 0
	}
	var walk func(n query.Node) int
	walk = func(n query.Node) int {
		if n.Type == "condition" {
			return 1
		}
		total := 0
		for _, ch := range n.Children {
			total += walk(ch)
		}
		return total
	}
	return walk(*f.Root)
}

// degrade rewrites a stored filter for the viewer's catalog (design: "Saved View with an unreadable field"). A
// condition the viewer may not use (unknown or restricted field, operator not offered, restricted enum value) is
// replaced by FALSE and reported, never dropped, so losing a permission can only narrow a result, never widen it.
// FALSE propagates: an AND group containing it is FALSE, an OR group simply loses that branch. empty is true when
// the whole filter can match nothing; the caller then returns no rows without running a query. Sort keys the
// viewer may not use are removed (a sort never widens a result).
func degrade(f query.Filter, info query.Info) (out query.Filter, warnings []query.Warning, empty bool) {
	fields := make(map[string]query.FieldInfo, len(info.Fields))
	for _, fi := range info.Fields {
		fields[fi.Key] = fi
	}
	out = f
	if f.Root != nil {
		n, ok := degradeNode(*f.Root, "root", fields, &warnings)
		if !ok {
			return query.Filter{V: 1}, warnings, true
		}
		out.Root = &n
	}
	if len(f.Sort) > 0 {
		out.Sort = nil
		for i, s := range f.Sort {
			if fi, ok := fields[s.Field]; ok && fi.Sortable {
				out.Sort = append(out.Sort, s)
				continue
			}
			warnings = append(warnings, query.Warning{Code: query.CodeFieldUnavailable, Path: fmt.Sprintf("sort[%d]", i)})
		}
	}
	return out, warnings, false
}

// degradeNode returns the rewritten node and false when the node can match nothing.
func degradeNode(n query.Node, path string, fields map[string]query.FieldInfo, warnings *[]query.Warning) (query.Node, bool) {
	if n.Type == "condition" {
		if conditionUsable(n, fields) {
			return n, true
		}
		*warnings = append(*warnings, query.Warning{Code: query.CodeFieldUnavailable, Path: path})
		return n, false
	}
	if n.Type != "group" {
		// Malformed stored data: let the module reject it with its own error.
		return n, true
	}
	kept := make([]query.Node, 0, len(n.Children))
	for i, ch := range n.Children {
		c, ok := degradeNode(ch, fmt.Sprintf("%s.children[%d]", path, i), fields, warnings)
		switch {
		case ok:
			kept = append(kept, c)
		case n.Logic == "and":
			return n, false
		}
	}
	if n.Logic == "or" && len(kept) == 0 && len(n.Children) > 0 {
		return n, false
	}
	n.Children = kept
	return n, true
}

// conditionUsable reports whether the viewer's catalog offers the field, the operator and the enum values used.
func conditionUsable(n query.Node, fields map[string]query.FieldInfo) bool {
	fi, ok := fields[n.Field]
	if !ok || !fi.Filterable || !slices.Contains(fi.Operators, query.Op(n.Op)) {
		return false
	}
	if len(fi.EnumValues) == 0 || (fi.Type != query.TypeEnum && fi.Type != query.TypeTags) || len(n.Value) == 0 {
		return true
	}
	var many []string
	if json.Unmarshal(n.Value, &many) != nil {
		var one string
		if json.Unmarshal(n.Value, &one) != nil {
			return true // not a string value: the module reports the type error
		}
		many = []string{one}
	}
	for _, v := range many {
		if !slices.Contains(fi.EnumValues, v) {
			return false
		}
	}
	return true
}
