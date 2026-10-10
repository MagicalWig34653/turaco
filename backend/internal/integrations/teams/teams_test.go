package teams_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/microsoft"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/teams"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/health"
)

const secretURL = "https://prod-12.westeurope.logic.azure.com:443/workflows/abc/triggers/manual/paths/invoke?sig=TOPSECRET"

func goodCard() teams.Card {
	return teams.Card{Kind: teams.KindDeclared, Category: "majorincident.update", Reference: "MI-000012",
		Headline: "A major incident was declared", Action: "Open in Turaco", LinkURL: "https://turaco.example.org/incidents/1", Locale: "en"}
}

func TestCardValidateIsReferenceOnly(t *testing.T) {
	if err := goodCard().Validate(); err != nil {
		t.Fatalf("good card: %v", err)
	}
	bad := map[string]func(*teams.Card){
		"reference with a title": func(c *teams.Card) { c.Reference = "MI-000012 Patient X dialysis outage" },
		"reference with newline": func(c *teams.Card) { c.Reference = "MI-1\nsecond line" },
		"empty reference":        func(c *teams.Card) { c.Reference = "" },
		"unknown kind":           func(c *teams.Card) { c.Kind = "resolved" },
		"empty headline":         func(c *teams.Card) { c.Headline = " " },
		"headline control char":  func(c *teams.Card) { c.Headline = "x\x00" },
		"link without scheme":    func(c *teams.Card) { c.LinkURL = "//evil.example/x" },
		"link javascript":        func(c *teams.Card) { c.LinkURL = "javascript:alert(1)" },
		"link with credentials":  func(c *teams.Card) { c.LinkURL = "https://u:p@turaco.example.org/x" },
		"unknown locale":         func(c *teams.Card) { c.Locale = "fr" },
		"overlong action":        func(c *teams.Card) { c.Action = strings.Repeat("a", 201) },
		"missing category":       func(c *teams.Card) { c.Category = "" },
	}
	for name, mutate := range bad {
		c := goodCard()
		mutate(&c)
		if err := c.Validate(); !errors.Is(err, teams.ErrInvalidCard) {
			t.Errorf("%s: err = %v, want ErrInvalidCard", name, err)
		}
	}
}

func TestParseDestinations(t *testing.T) {
	d, err := teams.ParseDestinations([]byte(`{"infra":"` + secretURL + `","ops":"https://x.12.environment.api.powerplatform.com/powerautomate/automations/direct/workflows/1/triggers/manual/paths/invoke?sig=s"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.Keys(), ","); got != "infra,ops" {
		t.Errorf("keys = %s", got)
	}
	if len(d.Hosts()) != 2 {
		t.Errorf("hosts = %v", d.Hosts())
	}
	bad := map[string]string{
		"not an object":    `["a"]`,
		"empty":            `{}`,
		"trailing":         `{"a":"` + secretURL + `"} {}`,
		"http":             `{"a":"http://prod-1.westeurope.logic.azure.com/x"}`,
		"foreign host":     `{"a":"https://evil.example.org/x"}`,
		"suffix trick":     `{"a":"https://logic.azure.com.evil.example.org/x"}`,
		"bare suffix":      `{"a":"https://logic.azure.com/x"}`,
		"credentials":      `{"a":"https://u:p@prod-1.westeurope.logic.azure.com/x"}`,
		"other port":       `{"a":"https://prod-1.westeurope.logic.azure.com:8443/x"}`,
		"upper case key":   `{"Infra":"` + secretURL + `"}`,
		"key with a space": `{"in fra":"` + secretURL + `"}`,
	}
	for name, content := range bad {
		_, err := teams.ParseDestinations([]byte(content))
		if err == nil {
			t.Errorf("%s: want error", name)
		} else if strings.Contains(err.Error(), "TOPSECRET") || strings.Contains(err.Error(), "sig=") {
			t.Errorf("%s: error leaks the URL: %v", name, err)
		}
	}
}

type stub struct {
	status int
	header http.Header
	err    error
	got    []*http.Request
	body   []byte
}

func (s *stub) RoundTrip(r *http.Request) (*http.Response, error) {
	s.got = append(s.got, r)
	if r.Body != nil {
		s.body, _ = io.ReadAll(r.Body)
	}
	if s.err != nil {
		return nil, s.err
	}
	h := s.header
	if h == nil {
		h = http.Header{}
	}
	return &http.Response{StatusCode: s.status, Header: h, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
}

func workflows(t *testing.T, s *stub) *teams.Workflows {
	t.Helper()
	d, err := teams.ParseDestinations([]byte(`{"infra":"` + secretURL + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	w, err := teams.NewWorkflows(d, microsoft.Config{Transport: s})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestWorkflowsPostsAnAdaptiveCard(t *testing.T) {
	s := &stub{status: http.StatusAccepted}
	w := workflows(t, s)
	if w.Mode() != health.ModeReal {
		t.Errorf("mode = %s", w.Mode())
	}
	if err := w.PostToChannel(context.Background(), "infra", goodCard()); err != nil {
		t.Fatal(err)
	}
	if len(s.got) != 1 || s.got[0].Method != http.MethodPost || s.got[0].URL.Query().Get("sig") != "TOPSECRET" {
		t.Fatalf("request = %+v", s.got)
	}
	var msg struct {
		Type        string `json:"type"`
		Attachments []struct {
			ContentType string `json:"contentType"`
			Content     struct {
				Type string `json:"type"`
				Body []struct {
					Text string `json:"text"`
				} `json:"body"`
				Actions []struct {
					URL string `json:"url"`
				} `json:"actions"`
			} `json:"content"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(s.body, &msg); err != nil {
		t.Fatal(err)
	}
	a := msg.Attachments[0]
	if msg.Type != "message" || a.ContentType != "application/vnd.microsoft.card.adaptive" || a.Content.Type != "AdaptiveCard" {
		t.Errorf("envelope = %s", s.body)
	}
	if a.Content.Body[0].Text != "A major incident was declared" || a.Content.Body[1].Text != "MI-000012" || a.Content.Actions[0].URL != "https://turaco.example.org/incidents/1" {
		t.Errorf("card = %s", s.body)
	}
	if st := w.Status(); st.LastSuccessAt == nil {
		t.Error("a successful post must be observed")
	}
}

func TestWorkflowsClassifiesFailures(t *testing.T) {
	tests := []struct {
		name      string
		stub      *stub
		code      string
		transient bool
		retry     time.Duration
	}{
		{"rate limited", &stub{status: 429, header: http.Header{"Retry-After": {"120"}}}, "rate_limited", true, 2 * time.Minute},
		{"server error", &stub{status: 503}, "http_503", true, 0},
		{"network", &stub{err: errors.New("connection reset")}, "network", true, 0},
		{"flow removed", &stub{status: 404}, "http_404", false, 0},
		{"forbidden", &stub{status: 403}, "http_403", false, 0},
		{"redirect is not followed", &stub{status: 302}, "http_302", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := workflows(t, tt.stub)
			err := w.PostToChannel(context.Background(), "infra", goodCard())
			if err == nil {
				t.Fatal("want error")
			}
			if got := teams.ErrorCode(err); got != tt.code {
				t.Errorf("code = %q, want %q", got, tt.code)
			}
			var te *teams.TransientError
			if errors.As(err, &te) != tt.transient {
				t.Errorf("transient = %v, want %v (%v)", !tt.transient, tt.transient, err)
			}
			if te != nil && te.RetryAfter != tt.retry {
				t.Errorf("retry after = %v, want %v", te.RetryAfter, tt.retry)
			}
			if strings.Contains(err.Error(), "TOPSECRET") || strings.Contains(err.Error(), "logic.azure.com") {
				t.Errorf("error leaks the webhook URL: %v", err)
			}
			if st := w.Status(); st.LastFailureAt == nil || st.LastErrorCode != tt.code {
				t.Errorf("status = %+v", st)
			}
		})
	}
}

func TestWorkflowsRefusesUnknownDestinationAndInvalidCard(t *testing.T) {
	s := &stub{status: 202}
	w := workflows(t, s)
	if err := w.PostToChannel(context.Background(), "nope", goodCard()); !errors.Is(err, teams.ErrUnknownDestination) {
		t.Errorf("unknown destination: %v", err)
	}
	c := goodCard()
	c.Reference = "MI-1 with title"
	if err := w.PostToChannel(context.Background(), "infra", c); !errors.Is(err, teams.ErrInvalidCard) {
		t.Errorf("invalid card: %v", err)
	}
	if len(s.got) != 0 {
		t.Error("nothing may be sent for a refused post")
	}
}

// senderContract is what every Sender must satisfy.
func senderContract(t *testing.T, s teams.Sender, key string, wantMode health.Mode, wantPost bool) {
	t.Helper()
	if s.Mode() != wantMode {
		t.Errorf("mode = %s, want %s", s.Mode(), wantMode)
	}
	err := s.PostToChannel(context.Background(), key, goodCard())
	if wantPost && err != nil {
		t.Errorf("post: %v", err)
	}
	if !wantPost && err == nil {
		t.Error("post must fail")
	}
	if err := s.PostToChannel(context.Background(), "missing", goodCard()); err == nil {
		t.Error("an unknown destination must fail")
	}
}

func TestSenderContract(t *testing.T) {
	fake := teams.NewFake("infra")
	senderContract(t, fake, "infra", health.ModeFake, true)
	if len(fake.Posted()) != 1 || fake.Posted()[0].DestinationKey != "infra" {
		t.Errorf("posted = %+v", fake.Posted())
	}
	senderContract(t, teams.NotConfigured{}, "infra", health.ModeNotConfigured, false)
	if err := (teams.NotConfigured{}).PostToChannel(context.Background(), "infra", goodCard()); !errors.Is(err, teams.ErrNotConfigured) {
		t.Errorf("not configured: %v", err)
	}
	senderContract(t, workflows(t, &stub{status: 202}), "infra", health.ModeReal, true)
}

func TestFakeInjectsFailures(t *testing.T) {
	f := teams.NewFake("infra")
	f.FailNext(&teams.TransientError{Code: "rate_limited", RetryAfter: time.Minute})
	err := f.PostToChannel(context.Background(), "infra", goodCard())
	var te *teams.TransientError
	if !errors.As(err, &te) || te.RetryAfter != time.Minute {
		t.Fatalf("err = %v", err)
	}
	if err := f.PostToChannel(context.Background(), "infra", goodCard()); err != nil {
		t.Errorf("the queue is consumed, next post succeeds: %v", err)
	}
}
