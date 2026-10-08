package ai_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
)

func (e *env) admin(perms ...string) ai.Caller { return e.caller(perms...) }

func validExternal() ai.ProviderInput {
	return ai.ProviderInput{Kind: "openai_compatible", DisplayName: "Cloud", EndpointURL: "https://api.example.com/v1", Model: "m1",
		DPARecordedOn: "2026-01-15", NoTrainingConfirmed: true, Region: "eu-central-1", SecretRef: "cloud-key"}
}

func TestProviderValidationAndEgressDefaults(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.admin("ai.settings.manage")
	if _, err := e.pool.Exec(ctx, `DELETE FROM ai.providers`); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*ai.ProviderInput){
		"plain http external": func(p *ai.ProviderInput) { p.EndpointURL = "http://api.example.com/v1" },
		"loopback external":   func(p *ai.ProviderInput) { p.EndpointURL = "https://127.0.0.1/v1" },
		"metadata local":      func(p *ai.ProviderInput) { p.Local, p.EndpointURL = true, "http://169.254.169.254/v1" },
		"userinfo":            func(p *ai.ProviderInput) { p.EndpointURL = "https://u:p@api.example.com/v1" },
		"no dpa":              func(p *ai.ProviderInput) { p.DPARecordedOn = "" },
		"future dpa":          func(p *ai.ProviderInput) { p.DPARecordedOn = "2999-01-01" },
		"no training":         func(p *ai.ProviderInput) { p.NoTrainingConfirmed = false },
		"no region":           func(p *ai.ProviderInput) { p.Region = "" },
		"bad class":           func(p *ai.ProviderInput) { p.AllowedDataClasses = []ai.DataClass{"secrets"} },
		"bad kind":            func(p *ai.ProviderInput) { p.Kind = "anthropic" },
		"secret path":         func(p *ai.ProviderInput) { p.SecretRef = "../../etc/passwd" },
		"no model":            func(p *ai.ProviderInput) { p.Model = "" },
		"negative price":      func(p *ai.ProviderInput) { p.PriceInPerMTok = -1 },
	}
	for name, mut := range bad {
		in := validExternal()
		mut(&in)
		var inv *ai.InvalidInputError
		if _, err := e.svc.CreateProvider(ctx, m, in); !errors.As(err, &inv) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := dbCount(t, e.pool, `SELECT count(*) FROM ai.providers`); n != 0 {
		t.Fatalf("%d invalid providers stored", n)
	}
	// Permissions: viewing is not managing; nobody without a key gets anything.
	if _, err := e.svc.CreateProvider(ctx, e.admin("ai.settings.view"), validExternal()); !errors.Is(err, ai.ErrForbidden) {
		t.Errorf("view cannot create: %v", err)
	}
	if _, err := e.svc.ListProviders(ctx, e.admin("ai.use")); !errors.Is(err, ai.ErrForbidden) {
		t.Errorf("ai.use cannot list providers: %v", err)
	}
	// Valid external provider: egress defaults to public_reference only, audited with before/after, event published.
	p, err := e.svc.CreateProvider(ctx, m, validExternal())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.AllowedDataClasses) != 1 || p.AllowedDataClasses[0] != ai.ClassPublicReference || p.Local || p.Enabled {
		t.Fatalf("defaults: %+v", p)
	}
	if n := dbCount(t, e.pool, `SELECT count(*) FROM platform.audit_events WHERE correlation_id=$1 AND action='ai.provider.created' AND tenant_id=$2`, m.CorrelationID, e.tenant); n != 1 {
		t.Errorf("provider creation audit entries: %d", n)
	}
	if n := dbCount(t, e.pool, `SELECT count(*) FROM platform.outbox_events WHERE correlation_id=$1 AND event_type='AIProviderChanged'`, m.CorrelationID); n != 1 {
		t.Errorf("provider event: %d", n)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id LIKE $1`, e.corr+"%")
	})
	// Only one provider may be enabled; enabling needs an explicit update; stale versions conflict.
	in := validExternal()
	in.Enabled = true
	v := p.Version
	p2, err := e.svc.UpdateProvider(ctx, m, p.ID, &v, in)
	if err != nil || !p2.Enabled || p2.Version != v+1 {
		t.Fatalf("enable: %+v %v", p2, err)
	}
	if _, err := e.svc.UpdateProvider(ctx, m, p.ID, &v, in); !errors.Is(err, ai.ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
	other := validExternal()
	other.Enabled, other.DisplayName = true, "Second"
	var inv *ai.InvalidInputError
	if _, err := e.svc.CreateProvider(ctx, m, other); !errors.As(err, &inv) || !strings.Contains(inv.Message, "already enabled") {
		t.Errorf("second enabled provider: %v", err)
	}
	// Disabling the active provider switches the assistant off.
	if _, err := e.pool.Exec(ctx, `UPDATE ai.settings SET enabled=true`); err != nil {
		t.Fatal(err)
	}
	in.Enabled = false
	v = p2.Version
	if _, err := e.svc.UpdateProvider(ctx, m, p.ID, &v, in); err != nil {
		t.Fatal(err)
	}
	st, _ := e.svc.GetSettings(ctx, m)
	if st.Enabled {
		t.Error("assistant stayed on without an enabled provider")
	}
	// The provider listing never carries secret material: only the reference name.
	list, err := e.svc.ListProviders(ctx, e.admin("ai.settings.view"))
	if err != nil || len(list) != 1 || list[0].SecretRef == nil || *list[0].SecretRef != "cloud-key" {
		t.Fatalf("list: %+v %v", list, err)
	}
	// A fake provider is local by construction and takes no endpoint.
	fk, err := e.svc.CreateProvider(ctx, m, ai.ProviderInput{Kind: "fake", DisplayName: "Fake", EndpointURL: "https://ignored.example"})
	if err != nil || !fk.Local || fk.EndpointURL != "" {
		t.Fatalf("fake: %+v %v", fk, err)
	}
}

func TestSettingsUpdateIsValidatedVersionedAndAudited(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.admin("ai.settings.manage")
	cur, err := e.svc.GetSettings(ctx, e.admin("ai.settings.view"))
	if err != nil {
		t.Fatal(err)
	}
	in := ai.SettingsInput{Enabled: true, RetainConversations: true, RetentionDays: 14, UserRequestsPerHour: 10, UserRequestsPerDay: 50, UserTokensPerDay: 100000,
		InstallationTokensPerDay: 400000, MaxOutputTokens: 512, MaxToolIterations: 4}
	if _, err := e.svc.UpdateSettings(ctx, e.admin("ai.settings.view"), in, &cur.Version); !errors.Is(err, ai.ErrForbidden) {
		t.Errorf("view cannot manage: %v", err)
	}
	if _, err := e.svc.UpdateSettings(ctx, m, in, nil); err == nil {
		t.Error("missing expectedVersion accepted")
	}
	for name, mut := range map[string]func(*ai.SettingsInput){
		"retention too long": func(s *ai.SettingsInput) { s.RetentionDays = 31 },
		"zero cap":           func(s *ai.SettingsInput) { s.UserRequestsPerHour = 0 },
		"too many iters":     func(s *ai.SettingsInput) { s.MaxToolIterations = 11 },
		"tiny output":        func(s *ai.SettingsInput) { s.MaxOutputTokens = 1 },
	} {
		bad := in
		mut(&bad)
		var inv *ai.InvalidInputError
		if _, err := e.svc.UpdateSettings(ctx, m, bad, &cur.Version); !errors.As(err, &inv) {
			t.Errorf("%s: %v", name, err)
		}
	}
	out, err := e.svc.UpdateSettings(ctx, m, in, &cur.Version)
	if err != nil || out.Version != cur.Version+1 || out.RetentionDays != 14 || out.UpdatedBy == nil || *out.UpdatedBy != m.UserID {
		t.Fatalf("update: %+v %v", out, err)
	}
	if _, err := e.svc.UpdateSettings(ctx, m, in, &cur.Version); !errors.Is(err, ai.ErrVersionConflict) {
		t.Errorf("stale: %v", err)
	}
	if n := dbCount(t, e.pool, `SELECT count(*) FROM platform.audit_events WHERE correlation_id=$1 AND action='ai.settings.updated' AND before_data IS NOT NULL AND after_data IS NOT NULL`, m.CorrelationID); n != 1 {
		t.Errorf("settings audit: %d", n)
	}
	// Switching on needs an enabled provider.
	if _, err := e.pool.Exec(ctx, `UPDATE ai.settings SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE ai.providers SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	cur, _ = e.svc.GetSettings(ctx, m)
	var inv *ai.InvalidInputError
	if _, err := e.svc.UpdateSettings(ctx, m, in, &cur.Version); !errors.As(err, &inv) {
		t.Errorf("enable without provider: %v", err)
	}
	// Usage needs its own permission.
	if _, err := e.svc.Usage(ctx, m, 7); !errors.Is(err, ai.ErrForbidden) {
		t.Errorf("usage without ai.usage.view: %v", err)
	}
	if _, err := e.svc.Usage(ctx, e.admin("ai.usage.view"), 7); err != nil {
		t.Errorf("usage: %v", err)
	}
}

func TestProviderTestReportsOnlyACode(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m := e.admin("ai.settings.manage")
	e.provider.set(scriptedFail())
	res, err := e.svc.TestProvider(ctx, m, e.provID)
	if err != nil || res.OK || res.Code != "provider_unavailable" {
		t.Fatalf("%+v %v", res, err)
	}
	e.provider.set(scriptedOK())
	res, err = e.svc.TestProvider(ctx, m, e.provID)
	if err != nil || !res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := e.svc.TestProvider(ctx, e.admin("ai.use"), e.provID); !errors.Is(err, ai.ErrForbidden) {
		t.Errorf("test without manage: %v", err)
	}
	if _, err := e.svc.TestProvider(ctx, m, "not-a-uuid"); !errors.Is(err, ai.ErrNotFound) {
		t.Errorf("bad id: %v", err)
	}
}
