package notifications

import (
	"strings"
	"testing"
)

func notif(category string, params map[string]any, linkType, linkID *string) Notification {
	return Notification{Category: category, Params: params, LinkType: linkType, LinkID: linkID}
}

func sp(s string) *string { return &s }

const taskID = "0192b6c0-0000-7000-8000-000000000001"

func TestRenderEmailLocalizesAndLinks(t *testing.T) {
	n := notif("task.assigned", map[string]any{"title": "Replace toner"}, sp("task"), sp(taskID))
	en, err := testReg(t).renderEmail("en", "https://turaco.example.org/", n)
	if err != nil {
		t.Fatal(err)
	}
	if en.Subject != "Task assigned: Replace toner" || !strings.Contains(en.Text, "https://turaco.example.org/tasks/"+taskID) ||
		!strings.Contains(en.HTML, `href="https://turaco.example.org/tasks/`+taskID+`"`) {
		t.Errorf("en = %+v", en)
	}
	de, err := testReg(t).renderEmail("de", "https://turaco.example.org", n)
	if err != nil || de.Subject != "Aufgabe zugewiesen: Replace toner" || !strings.Contains(de.Text, "Aufgabe öffnen") {
		t.Errorf("de = %+v %v", de, err)
	}
	if fb, _ := testReg(t).renderEmail("fr", "https://x.example", n); fb.Subject != en.Subject {
		t.Errorf("unknown locale must fall back to English: %q", fb.Subject)
	}
}

func TestRenderEmailEscapesUserText(t *testing.T) {
	evil := `<script>alert(1)</script> & "quotes"`
	r, err := testReg(t).renderEmail("en", "https://turaco.example.org", notif("task.assigned", map[string]any{"title": evil}, sp("task"), sp(taskID)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.HTML, "<script>") || !strings.Contains(r.HTML, "&lt;script&gt;") || !strings.Contains(r.HTML, "&amp;") {
		t.Errorf("title not escaped in HTML: %s", r.HTML)
	}
	if !strings.Contains(r.Text, evil) {
		t.Error("the plain text part keeps the title verbatim")
	}
}

func TestRenderEmailNeutralizesControlCharactersAndLongTitles(t *testing.T) {
	r, err := testReg(t).renderEmail("en", "https://turaco.example.org", notif("task.assigned",
		map[string]any{"title": "Hi\r\nBcc: x@example.org\x00" + strings.Repeat("é", 500)}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(r.Subject, "\r\n\x00") {
		t.Errorf("subject has control characters: %q", r.Subject)
	}
	if n := len([]rune(r.Subject)); n > 260 {
		t.Errorf("subject has %d characters", n)
	}
}

func TestRenderEmailLinksOnlyKnownTargets(t *testing.T) {
	for name, n := range map[string]Notification{
		"unknown link type": notif("task.assigned", map[string]any{"title": "t"}, sp("asset"), sp(taskID)),
		"malformed id":      notif("task.assigned", map[string]any{"title": "t"}, sp("task"), sp("../../admin")),
		"no link":           notif("task.assigned", map[string]any{"title": "t"}, nil, nil),
	} {
		r, err := testReg(t).renderEmail("en", "https://turaco.example.org", n)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(r.Text, "https://") || strings.Contains(r.HTML, "<a ") {
			t.Errorf("%s: unexpected link: %s", name, r.Text)
		}
	}
}

func TestRenderEmailRejectsUnknownCategory(t *testing.T) {
	if _, err := testReg(t).renderEmail("en", "https://x.example", notif("nope", nil, nil, nil)); err == nil {
		t.Error("an unknown category has no template")
	}
}

func testReg(t *testing.T) *Registry {
	t.Helper()
	text := func(subject, intro, action string) EmailText {
		return EmailText{Subject: subject, Intro: intro, Action: action}
	}
	r, err := NewRegistry(
		Category{Name: "task.assigned", Owner: "tasks", LinkType: "task", LinkPath: "/tasks/{id}", Email: map[string]EmailText{
			"en": text("Task assigned: %s", "A task was assigned to you:", "Open task"),
			"de": text("Aufgabe zugewiesen: %s", "Dir wurde eine Aufgabe zugewiesen:", "Aufgabe öffnen"),
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRegistryValidation(t *testing.T) {
	ok := EmailText{Subject: "S %s", Intro: "I", Action: "A"}
	both := map[string]EmailText{"en": ok, "de": ok}
	good := Category{Name: "task.assigned", Owner: "tasks", LinkType: "task", LinkPath: "/tasks/{id}", Email: both}
	if _, err := NewRegistry(good); err != nil {
		t.Fatal(err)
	}
	bad := map[string][]Category{
		"bad name":             {{Name: "Task", Owner: "x", Email: both}},
		"no owner":             {{Name: "a.b", Email: both}},
		"missing german":       {{Name: "a.b", Owner: "x", Email: map[string]EmailText{"en": ok}}},
		"subject without %s":   {{Name: "a.b", Owner: "x", Email: map[string]EmailText{"en": ok, "de": {Subject: "S", Intro: "I", Action: "A"}}}},
		"empty action":         {{Name: "a.b", Owner: "x", Email: map[string]EmailText{"en": ok, "de": {Subject: "S %s", Intro: "I"}}}},
		"duplicate":            {good, good},
		"link type only":       {{Name: "a.b", Owner: "x", Email: both, LinkType: "t"}},
		"path without id":      {{Name: "a.b", Owner: "x", Email: both, LinkType: "t", LinkPath: "/t"}},
		"path not absolute":    {{Name: "a.b", Owner: "x", Email: both, LinkType: "t", LinkPath: "t/{id}"}},
		"conflicting link map": {good, {Name: "a.c", Owner: "x", Email: both, LinkType: "task", LinkPath: "/other/{id}"}},
	}
	for name, cats := range bad {
		if _, err := NewRegistry(cats...); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	r, _ := NewRegistry(good, Category{Name: "a.b", Owner: "x", Email: both})
	if !r.Valid("a.b") || r.Valid("a.c") || len(r.Names()) != 2 || r.Names()[0] != "a.b" {
		t.Errorf("registry lookups: %v", r.Names())
	}
}

func TestLinkPathsComeFromTheRegistry(t *testing.T) {
	r := testReg(t)
	if got := r.linkPath("task", taskID); got != "/tasks/"+taskID {
		t.Errorf("path = %q", got)
	}
	if r.linkPath("asset", taskID) != "" || r.linkPath("task", "../x") != "" {
		t.Error("unknown link types and malformed ids must have no path")
	}
}
