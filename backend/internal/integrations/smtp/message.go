package smtp

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"time"
	"unicode"
)

const maxSubjectRunes = 200

// sanitizeSubject removes control characters (header injection) and bounds
// the length; the result is non-empty or an error.
func sanitizeSubject(s string) (string, error) {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) {
			r = ' '
		}
		b.WriteRune(r)
		if n++; n >= maxSubjectRunes {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", errors.New("smtp: subject is required")
	}
	return out, nil
}

func buildMessage(from, to *mail.Address, m Message, now time.Time) ([]byte, error) {
	subject, err := sanitizeSubject(m.Subject)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(m.Text) == "" || strings.TrimSpace(m.HTML) == "" {
		return nil, errors.New("smtp: both a text and an HTML body are required")
	}
	idBytes := make([]byte, 12)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, fmt.Errorf("smtp: message id: %w", err)
	}
	domain := "localhost"
	if i := strings.LastIndexByte(from.Address, '@'); i >= 0 {
		domain = from.Address[i+1:]
	}

	// The multipart body is built first because its boundary goes into the headers.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := writePart(mw, "text/plain", m.Text); err != nil {
		return nil, err
	}
	if err := writePart(mw, "text/html", m.HTML); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&buf, "%s: %s\r\n", k, v) }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", subject))
	header("Date", now.UTC().Format(time.RFC1123Z))
	header("Message-ID", fmt.Sprintf("<%s@%s>", hex.EncodeToString(idBytes), domain))
	header("MIME-Version", "1.0")
	header("Auto-Submitted", "auto-generated")
	header("Content-Type", fmt.Sprintf("multipart/alternative; boundary=%q", mw.Boundary()))
	buf.WriteString("\r\n")
	buf.Write(body.Bytes())
	return buf.Bytes(), nil
}

func writePart(mw *multipart.Writer, contentType, content string) error {
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", contentType+`; charset="utf-8"`)
	h.Set("Content-Transfer-Encoding", "quoted-printable")
	w, err := mw.CreatePart(h)
	if err != nil {
		return fmt.Errorf("smtp: create part: %w", err)
	}
	qw := quotedprintable.NewWriter(w)
	if _, err := qw.Write([]byte(content)); err != nil {
		return fmt.Errorf("smtp: encode part: %w", err)
	}
	return qw.Close()
}
