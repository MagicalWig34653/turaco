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
	en, err := renderEmail("en", "https://turaco.example.org/", n)
	if err != nil {
		t.Fatal(err)
	}
	if en.Subject != "Task assigned: Replace toner" || !strings.Contains(en.Text, "https://turaco.example.org/tasks/"+taskID) ||
		!strings.Contains(en.HTML, `href="https://turaco.example.org/tasks/`+taskID+`"`) {
		t.Errorf("en = %+v", en)
	}
	de, err := renderEmail("de", "https://turaco.example.org", n)
	if err != nil || de.Subject != "Aufgabe zugewiesen: Replace toner" || !strings.Contains(de.Text, "Aufgabe öffnen") {
		t.Errorf("de = %+v %v", de, err)
	}
	if fb, _ := renderEmail("fr", "https://x.example", n); fb.Subject != en.Subject {
		t.Errorf("unknown locale must fall back to English: %q", fb.Subject)
	}
}

func TestRenderEmailEscapesUserText(t *testing.T) {
	evil := `<script>alert(1)</script> & "quotes"`
	r, err := renderEmail("en", "https://turaco.example.org", notif("task.assigned", map[string]any{"title": evil}, sp("task"), sp(taskID)))
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
	r, err := renderEmail("en", "https://turaco.example.org", notif("task.assigned",
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
		r, err := renderEmail("en", "https://turaco.example.org", n)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(r.Text, "https://") || strings.Contains(r.HTML, "<a ") {
			t.Errorf("%s: unexpected link: %s", name, r.Text)
		}
	}
}

func TestRenderEmailRejectsUnknownCategory(t *testing.T) {
	if _, err := renderEmail("en", "https://x.example", notif("nope", nil, nil, nil)); err == nil {
		t.Error("an unknown category has no template")
	}
}

func TestEveryRegisteredCategoryHasTemplatesInAllLocales(t *testing.T) {
	for locale, tpls := range emailTemplates {
		for _, c := range Categories {
			tpl, ok := tpls[c]
			if !ok || tpl.subject == "" || tpl.intro == "" || tpl.action == "" || !strings.Contains(tpl.subject, "%s") {
				t.Errorf("locale %s category %s: incomplete template %+v", locale, c, tpl)
			}
		}
	}
	if len(emailTemplates["en"]) != len(emailTemplates["de"]) {
		t.Error("EN and DE templates differ")
	}
}
