package notifications

import (
	"fmt"
	"html"
	"strings"
)

// Email texts live here, not in the client: an email is rendered by the
// worker. Parameters are escaped for HTML and never trusted; a task title is
// the only user-supplied text and no descriptions are included.

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
func (r *Registry) renderEmail(locale, baseURL string, n Notification) (RenderedEmail, error) {
	cat, ok := r.Lookup(n.Category)
	if !ok {
		return RenderedEmail{}, fmt.Errorf("notifications: no email template for category %q", n.Category)
	}
	tpl, ok := cat.Email[locale]
	if !ok {
		locale, tpl = "en", cat.Email["en"]
	}
	title := titleParam(n.Params)
	footer := emailFooterEN
	if locale == "de" {
		footer = emailFooterDE
	}
	var link string
	if n.LinkType != nil && n.LinkID != nil {
		if p := r.linkPath(*n.LinkType, *n.LinkID); p != "" {
			link = strings.TrimRight(baseURL, "/") + p
		}
	}

	text := tpl.Intro + "\n\n" + title + "\n"
	htmlBody := `<p>` + html.EscapeString(tpl.Intro) + `</p><p><strong>` + html.EscapeString(title) + `</strong></p>`
	if link != "" {
		text += "\n" + tpl.Action + ": " + link + "\n"
		htmlBody += `<p><a href="` + html.EscapeString(link) + `">` + html.EscapeString(tpl.Action) + `</a></p>`
	}
	text += "\n-- \n" + footer + "\n"
	htmlBody += `<p style="color:#666;font-size:12px">` + html.EscapeString(footer) + `</p>`
	return RenderedEmail{
		Subject: fmt.Sprintf(tpl.Subject, title),
		Text:    text,
		HTML:    `<!doctype html><html><body style="font-family:sans-serif">` + htmlBody + `</body></html>`,
	}, nil
}
