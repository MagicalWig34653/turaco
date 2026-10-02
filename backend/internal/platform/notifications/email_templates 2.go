package notifications

import (
	"fmt"
	"html"
	"net/url"
	"strings"
)

// Email texts live here, not in the client: an email is rendered by the
// worker. Parameters are escaped for HTML and never trusted; a task title is
// the only user-supplied text and no descriptions are included.

type emailTemplate struct {
	subject string
	intro   string // %s = title
	action  string // link label
}

var emailTemplates = map[string]map[string]emailTemplate{
	"en": {
		"task.assigned":  {"Task assigned: %s", "A task was assigned to you:", "Open task"},
		"task.completed": {"Task completed: %s", "A task you created was completed:", "Open task"},
	},
	"de": {
		"task.assigned":  {"Aufgabe zugewiesen: %s", "Dir wurde eine Aufgabe zugewiesen:", "Aufgabe öffnen"},
		"task.completed": {"Aufgabe erledigt: %s", "Eine von dir angelegte Aufgabe wurde erledigt:", "Aufgabe öffnen"},
	},
}

const (
	emailFooterEN = "You receive this message because of your notification settings in Turaco."
	emailFooterDE = "Du erhältst diese Nachricht aufgrund deiner Benachrichtigungseinstellungen in Turaco."
)

// RenderedEmail is the subject and both bodies of a notification email.
type RenderedEmail struct {
	Subject string
	Text    string
	HTML    string
}

// linkPath maps a notification link to an in-app path; unknown link types
// have none, and ids are validated so nothing user-controlled reaches a URL.
func linkPath(linkType, linkID string) string {
	if linkType == "task" && validUUID(linkID) {
		return "/tasks/" + url.PathEscape(linkID)
	}
	return ""
}

func titleParam(params map[string]any) string {
	title, _ := params["title"].(string)
	title = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, title)
	title = strings.TrimSpace(title)
	if len([]rune(title)) > 200 {
		title = string([]rune(title)[:200])
	}
	return title
}

// renderEmail builds the email of a notification. locale falls back to "en";
// an unknown category is an error (the caller treats it as permanent).
func renderEmail(locale, baseURL string, n Notification) (RenderedEmail, error) {
	tpls, ok := emailTemplates[locale]
	if !ok {
		locale, tpls = "en", emailTemplates["en"]
	}
	tpl, ok := tpls[n.Category]
	if !ok {
		return RenderedEmail{}, fmt.Errorf("notifications: no email template for category %q", n.Category)
	}
	title := titleParam(n.Params)
	footer := emailFooterEN
	if locale == "de" {
		footer = emailFooterDE
	}
	var link string
	if n.LinkType != nil && n.LinkID != nil {
		if p := linkPath(*n.LinkType, *n.LinkID); p != "" {
			link = strings.TrimRight(baseURL, "/") + p
		}
	}

	text := tpl.intro + "\n\n" + title + "\n"
	htmlBody := `<p>` + html.EscapeString(tpl.intro) + `</p><p><strong>` + html.EscapeString(title) + `</strong></p>`
	if link != "" {
		text += "\n" + tpl.action + ": " + link + "\n"
		htmlBody += `<p><a href="` + html.EscapeString(link) + `">` + html.EscapeString(tpl.action) + `</a></p>`
	}
	text += "\n-- \n" + footer + "\n"
	htmlBody += `<p style="color:#666;font-size:12px">` + html.EscapeString(footer) + `</p>`
	return RenderedEmail{
		Subject: fmt.Sprintf(tpl.subject, title),
		Text:    text,
		HTML:    `<!doctype html><html><body style="font-family:sans-serif">` + htmlBody + `</body></html>`,
	}, nil
}
