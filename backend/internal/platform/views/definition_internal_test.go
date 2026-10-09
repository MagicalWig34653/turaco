package views

import (
	"encoding/json"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

func infoOf(keys ...string) query.Info {
	var info query.Info
	for _, k := range keys {
		info.Fields = append(info.Fields, query.FieldInfo{Key: k, Type: query.TypeText, Filterable: true, Sortable: true,
			Operators: []query.Op{query.OpEquals, query.OpContains}})
	}
	return info
}

func cond(field string) query.Node {
	return query.Cond(field, query.OpEquals, "x")
}

func group(logic string, ch ...query.Node) query.Node {
	return query.Node{Type: "group", Logic: logic, Children: ch}
}

// FALSE semantics: an AND group with an unusable condition is FALSE, an OR group loses the branch, and the
// result never has fewer restrictions than the stored filter.
func TestDegradeSemantics(t *testing.T) {
	cases := []struct {
		name      string
		root      query.Node
		have      []string
		wantEmpty bool
		wantWarn  int
		wantKids  int // children of the resulting root (groups only)
	}{
		{"all usable", group("and", cond("a"), cond("b")), []string{"a", "b"}, false, 0, 2},
		{"and with an unusable child", group("and", cond("a"), cond("z")), []string{"a"}, true, 1, 0},
		{"or loses the branch", group("or", cond("a"), cond("z")), []string{"a"}, false, 1, 1},
		{"or of only unusable", group("or", cond("y"), cond("z")), []string{"a"}, true, 2, 0},
		{"nested: the or inside an and survives", group("and", cond("a"), group("or", cond("b"), cond("z"))), []string{"a", "b"}, false, 1, 2},
		{"nested: the and inside an or dies", group("or", cond("a"), group("and", cond("b"), cond("z"))), []string{"a", "b"}, false, 1, 1},
		{"single unusable condition at the root", cond("z"), []string{"a"}, true, 1, 0},
		{"operator not offered", group("and", query.Cond("a", query.OpBefore, "x")), []string{"a"}, true, 1, 0},
	}
	for _, c := range cases {
		root := c.root
		out, warns, empty := degrade(query.Filter{V: 1, Root: &root}, infoOf(c.have...))
		if empty != c.wantEmpty || len(warns) != c.wantWarn {
			t.Errorf("%s: empty=%v warnings=%d, want %v/%d", c.name, empty, len(warns), c.wantEmpty, c.wantWarn)
			continue
		}
		if !empty && out.Root.Type == "group" && len(out.Root.Children) != c.wantKids {
			t.Errorf("%s: %d children, want %d", c.name, len(out.Root.Children), c.wantKids)
		}
		for _, w := range warns {
			if w.Code != query.CodeFieldUnavailable || w.Path == "" {
				t.Errorf("%s: warning %+v", c.name, w)
			}
		}
	}
}

func TestDegradeDoesNotMutateTheStoredFilter(t *testing.T) {
	root := group("or", cond("a"), cond("z"))
	f := query.Filter{V: 1, Root: &root}
	degrade(f, infoOf("a"))
	if len(root.Children) != 2 {
		t.Error("the stored filter was modified")
	}
}

func TestCanonicalIsStableAndBounded(t *testing.T) {
	mk := func(raw string) Definition {
		f, err := query.DecodeFilter([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return Definition{Filter: f, Columns: []string{"title", "status"}}
	}
	_, a, ha, err := canonical(mk(`{"v":1,"root":{"type":"condition","field":"status","op":"in","value":[ "open", "closed" ]},"search":"  printer "}`))
	if err != nil {
		t.Fatal(err)
	}
	_, b, hb, err := canonical(mk(`{"v":1,"search":"printer","root":{"type":"condition","field":"status","op":"in","value":["open","closed"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) || ha != hb {
		t.Errorf("equivalent filters differ:\n%s\n%s", a, b)
	}
	var back Definition
	if err := json.Unmarshal(a, &back); err != nil || back.Filter.Search != "printer" {
		t.Errorf("round trip: %v %+v", err, back)
	}
	// An empty filter canonicalizes to no filter at all.
	d, _, _, err := canonical(Definition{Filter: &query.Filter{V: 1}})
	if err != nil || d.Filter != nil {
		t.Errorf("empty filter kept: %v %+v", err, d.Filter)
	}
}
