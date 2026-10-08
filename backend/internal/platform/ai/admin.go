package ai

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai/safehttp"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// ---- conversation housekeeping ----

// Consent adds one record to the conversation's resource scope after the User explicitly allowed it (A11).
// Tool output never does this by itself.
func (s *Service) Consent(ctx context.Context, c Caller, conversationID string, ref ResourceRef) error {
	if _, err := s.gate(ctx, c); err != nil {
		return err
	}
	if !slices.Contains(s.reg.TargetTypes(), ref.Type) || !uuidRE.MatchString(ref.ID) {
		return invalid("resourceType must be a known type and resourceId a UUID")
	}
	se, err := s.loadSession(ctx, c, conversationID)
	if err != nil {
		return err
	}
	ref.ID = strings.ToLower(ref.ID)
	if len(se.Scope) >= maxScopeEntries {
		return ErrContextTooLarge
	}
	se.Scope = addScope(se.Scope, ref)
	if err := s.store.saveScope(ctx, se); err != nil {
		return err
	}
	return s.auditTurn(ctx, c, "ai.scope.consented", se.ID, map[string]any{"target": map[string]any{"type": ref.Type, "id": ref.ID}})
}

// TranscriptMessage is a User-visible message of a conversation (user and assistant text only).
type TranscriptMessage struct {
	Role    string
	Content string
}

// Transcript returns the visible messages of a conversation the caller owns. Tool calls and results stay on the server.
func (s *Service) Transcript(ctx context.Context, c Caller, conversationID string) ([]TranscriptMessage, error) {
	if _, err := s.gate(ctx, c); err != nil {
		return nil, err
	}
	se, err := s.loadSession(ctx, c, conversationID)
	if err != nil {
		return nil, err
	}
	out := []TranscriptMessage{}
	for _, m := range se.Transcript {
		if (m.Role == "user" || m.Role == "assistant") && m.Content != "" && len(m.ToolCalls) == 0 {
			out = append(out, TranscriptMessage{Role: m.Role, Content: m.Content})
		}
	}
	return out, nil
}

// EndConversation deletes the server-held session of a conversation the caller owns.
func (s *Service) EndConversation(ctx context.Context, c Caller, conversationID string) error {
	if !s.cfg.Enabled {
		return ErrDisabled
	}
	if c.UserID == "" || c.TenantID == "" || !uuidRE.MatchString(c.SessionID) || !c.Has(PermUse) {
		return ErrForbidden
	}
	se, err := s.loadSession(ctx, c, conversationID)
	if err != nil {
		return err
	}
	if err := s.store.deleteSession(ctx, se.ID); err != nil {
		return err
	}
	return s.auditTurn(ctx, c, "ai.conversation.ended", se.ID, nil)
}

// ---- settings ----

// requireAdmin authorizes configuration access. It deliberately ignores the startup gate: administrators prepare
// settings and providers before enabling the module. Only provider calls and conversations are gated.
func (s *Service) requireAdmin(c Caller, manage bool) error {
	if c.UserID == "" || c.TenantID == "" {
		return ErrForbidden
	}
	if manage && !c.Has(PermSettingsManage) {
		return ErrForbidden
	}
	if !manage && !c.Has(PermSettingsView) && !c.Has(PermSettingsManage) {
		return ErrForbidden
	}
	return nil
}

// GetSettings returns the AI policy. Needs ai.settings.view or ai.settings.manage.
func (s *Service) GetSettings(ctx context.Context, c Caller) (Settings, error) {
	if err := s.requireAdmin(c, false); err != nil {
		return Settings{}, err
	}
	return s.store.GetSettings(ctx)
}

// SettingsInput is the replaceable policy. Needs ai.settings.manage.
type SettingsInput struct {
	Enabled                  bool
	RetainConversations      bool
	RetentionDays            int
	UserRequestsPerHour      int
	UserRequestsPerDay       int
	UserTokensPerDay         int
	InstallationTokensPerDay int
	MaxOutputTokens          int
	MaxToolIterations        int
}

func between(v, lo, hi int) bool { return v >= lo && v <= hi }

// UpdateSettings replaces the policy (audited, optimistic version).
func (s *Service) UpdateSettings(ctx context.Context, c Caller, in SettingsInput, expected *int) (Settings, error) {
	if err := s.requireAdmin(c, true); err != nil {
		return Settings{}, err
	}
	if expected == nil {
		return Settings{}, invalid("expectedVersion is required")
	}
	switch {
	case !between(in.RetentionDays, 1, 30):
		return Settings{}, invalid("retentionDays must be between 1 and 30")
	case !between(in.UserRequestsPerHour, 1, 10000), !between(in.UserRequestsPerDay, 1, 100000):
		return Settings{}, invalid("request caps are out of range")
	case !between(in.UserTokensPerDay, 1000, 100000000), !between(in.InstallationTokensPerDay, 1000, 1000000000):
		return Settings{}, invalid("token caps are out of range")
	case !between(in.MaxOutputTokens, 64, 8192), !between(in.MaxToolIterations, 1, 10):
		return Settings{}, invalid("maxOutputTokens or maxToolIterations is out of range")
	}
	var out Settings
	err := s.store.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.lockSettings(ctx, tx)
		if err != nil {
			return err
		}
		if cur.Version != *expected {
			return ErrVersionConflict
		}
		if in.Enabled && !cur.Enabled {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM ai.providers WHERE enabled`).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return invalid("enable a provider before switching the assistant on")
			}
		}
		next := Settings{Enabled: in.Enabled, RetainConversations: in.RetainConversations, RetentionDays: in.RetentionDays,
			UserRequestsPerHour: in.UserRequestsPerHour, UserRequestsPerDay: in.UserRequestsPerDay, UserTokensPerDay: in.UserTokensPerDay,
			InstallationTokensPerDay: in.InstallationTokensPerDay, MaxOutputTokens: in.MaxOutputTokens, MaxToolIterations: in.MaxToolIterations}
		if out, err = s.store.updateSettings(ctx, tx, next, c.UserID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{Action: "ai.settings.updated", TargetType: "ai_settings", TargetID: "singleton",
			Actor: audit.UserActor(c.UserID), CorrelationID: c.CorrelationID, TenantID: c.TenantID,
			Before: settingsAudit(cur), After: settingsAudit(out)})
	})
	return out, err
}

func settingsAudit(st Settings) map[string]any {
	return map[string]any{"enabled": st.Enabled, "retainConversations": st.RetainConversations, "retentionDays": st.RetentionDays,
		"userRequestsPerHour": st.UserRequestsPerHour, "userRequestsPerDay": st.UserRequestsPerDay, "userTokensPerDay": st.UserTokensPerDay,
		"installationTokensPerDay": st.InstallationTokensPerDay, "maxOutputTokens": st.MaxOutputTokens, "maxToolIterations": st.MaxToolIterations,
		"version": st.Version}
}

// ---- providers ----

// ProviderInput is the writable part of an AI Provider. The credential itself is never accepted: SecretRef names a
// deployment secret file (ADR-0014); the API is write-only for the reference and never returns secret material.
type ProviderInput struct {
	Kind                string
	DisplayName         string
	EndpointURL         string
	Model               string
	Local               bool
	AllowedDataClasses  []DataClass // nil selects public_reference only (egress default, open decision 2)
	DPARecordedOn       string
	NoTrainingConfirmed bool
	Region              string
	SecretRef           string
	Enabled             bool
	PriceInPerMTok      float64
	PriceOutPerMTok     float64
}

var (
	regionRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,39}$`)
	secretRefRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

func (in ProviderInput) record() (ProviderRecord, error) {
	p := ProviderRecord{Kind: in.Kind, DisplayName: strings.TrimSpace(in.DisplayName), EndpointURL: strings.TrimSpace(in.EndpointURL),
		Model: strings.TrimSpace(in.Model), Local: in.Local, NoTrainingConfirm: in.NoTrainingConfirmed, Region: strings.TrimSpace(in.Region),
		Enabled: in.Enabled, PriceInPerMTok: in.PriceInPerMTok, PriceOutPerMTok: in.PriceOutPerMTok}
	if n := utf8.RuneCountInString(p.DisplayName); n < 1 || n > 100 || !utf8.ValidString(p.DisplayName) || strings.ContainsAny(p.DisplayName, "\x00\r\n\t") {
		return p, invalid("displayName must be 1 to 100 characters")
	}
	if utf8.RuneCountInString(p.Model) > 200 || strings.ContainsAny(p.Model, "\x00\r\n\t ") {
		return p, invalid("model is invalid")
	}
	if p.PriceInPerMTok < 0 || p.PriceOutPerMTok < 0 || p.PriceInPerMTok > 100000 || p.PriceOutPerMTok > 100000 {
		return p, invalid("prices are out of range")
	}
	switch p.Kind {
	case "fake":
		p.Local, p.EndpointURL = true, ""
	case "openai_compatible":
		if _, err := safehttp.ValidateEndpoint(p.EndpointURL, p.Local); err != nil {
			return p, invalid("endpointUrl is not allowed: %s", err.Error())
		}
		if p.Model == "" {
			return p, invalid("model is required")
		}
	default:
		return p, invalid("kind must be fake or openai_compatible")
	}
	if in.SecretRef != "" {
		if !secretRefRE.MatchString(in.SecretRef) {
			return p, invalid("secretRef must name a deployment secret (lower-case letters, digits, - and _)")
		}
		ref := in.SecretRef
		p.SecretRef = &ref
	}
	classes := in.AllowedDataClasses
	if classes == nil {
		classes = []DataClass{ClassPublicReference}
	}
	for _, cl := range classes {
		if !cl.Valid() {
			return p, invalid("unknown data class")
		}
		if !slices.Contains(p.AllowedDataClasses, cl) {
			p.AllowedDataClasses = append(p.AllowedDataClasses, cl)
		}
	}
	slices.Sort(p.AllowedDataClasses)
	if p.AllowedDataClasses == nil {
		p.AllowedDataClasses = []DataClass{}
	}
	if !p.Local {
		// External providers need a recorded data processing agreement, the no-training confirmation and a region.
		d, err := time.Parse(time.DateOnly, in.DPARecordedOn)
		if err != nil || d.After(time.Now().UTC().AddDate(0, 0, 1)) {
			return p, invalid("an external provider needs dpaRecordedOn as a past or current date")
		}
		ds := in.DPARecordedOn
		p.DPARecordedOn = &ds
		if !p.NoTrainingConfirm || !regionRE.MatchString(p.Region) {
			return p, invalid("an external provider needs noTrainingConfirmed and a region")
		}
	} else if in.DPARecordedOn != "" {
		if _, err := time.Parse(time.DateOnly, in.DPARecordedOn); err != nil {
			return p, invalid("dpaRecordedOn must be a date")
		}
		ds := in.DPARecordedOn
		p.DPARecordedOn = &ds
	}
	return p, nil
}

// ListProviders returns the providers without secret material. Needs ai.settings.view or manage.
func (s *Service) ListProviders(ctx context.Context, c Caller) ([]ProviderRecord, error) {
	if err := s.requireAdmin(c, false); err != nil {
		return nil, err
	}
	return s.store.ListProviders(ctx)
}

func providerAudit(p ProviderRecord) map[string]any {
	return map[string]any{"kind": p.Kind, "displayName": p.DisplayName, "endpointUrl": p.EndpointURL, "model": p.Model, "local": p.Local,
		"allowedDataClasses": p.AllowedDataClasses, "dpaRecordedOn": p.DPARecordedOn, "noTrainingConfirmed": p.NoTrainingConfirm,
		"region": p.Region, "secretRef": p.SecretRef, "enabled": p.Enabled, "version": p.Version}
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// CreateProvider adds an AI Provider (audited, event AIProviderChanged). Needs ai.settings.manage.
func (s *Service) CreateProvider(ctx context.Context, c Caller, in ProviderInput) (ProviderRecord, error) {
	if err := s.requireAdmin(c, true); err != nil {
		return ProviderRecord{}, err
	}
	rec, err := in.record()
	if err != nil {
		return ProviderRecord{}, err
	}
	var out ProviderRecord
	err = s.store.inTx(ctx, func(tx pgx.Tx) error {
		if out, err = s.store.insertProvider(ctx, tx, rec, c.UserID); err != nil {
			if isUnique(err) {
				return invalid("another provider is already enabled; disable it first")
			}
			return fmt.Errorf("insert ai provider: %w", err)
		}
		return s.providerChanged(ctx, tx, c, "created", nil, &out)
	})
	return out, err
}

// UpdateProvider replaces an AI Provider's configuration with an optimistic version check.
func (s *Service) UpdateProvider(ctx context.Context, c Caller, id string, expected *int, in ProviderInput) (ProviderRecord, error) {
	if err := s.requireAdmin(c, true); err != nil {
		return ProviderRecord{}, err
	}
	if expected == nil {
		return ProviderRecord{}, invalid("expectedVersion is required")
	}
	if !uuidRE.MatchString(id) {
		return ProviderRecord{}, ErrNotFound
	}
	rec, err := in.record()
	if err != nil {
		return ProviderRecord{}, err
	}
	var out ProviderRecord
	err = s.store.inTx(ctx, func(tx pgx.Tx) error {
		before, err := scanProvider(tx.QueryRow(ctx, `SELECT `+providerCols+` FROM ai.providers WHERE id=$1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if before.Kind != rec.Kind {
			return invalid("the provider kind cannot be changed")
		}
		if out, err = s.store.updateProvider(ctx, tx, id, *expected, rec, c.UserID); err != nil {
			if isUnique(err) {
				return invalid("another provider is already enabled; disable it first")
			}
			return err
		}
		if before.Enabled && !out.Enabled {
			// The assistant cannot stay on without a provider.
			if _, err := tx.Exec(ctx, `UPDATE ai.settings SET enabled=false, version=version+1, updated_at=now(), updated_by=$1 WHERE singleton AND enabled`, c.UserID); err != nil {
				return err
			}
		}
		return s.providerChanged(ctx, tx, c, "updated", &before, &out)
	})
	if err == nil {
		s.mu.Lock()
		delete(s.cache, id)
		s.mu.Unlock()
	}
	return out, err
}

func (s *Service) providerChanged(ctx context.Context, tx pgx.Tx, c Caller, op string, before, after *ProviderRecord) error {
	var b, a any
	if before != nil {
		b = providerAudit(*before)
	}
	if after != nil {
		a = providerAudit(*after)
	}
	if err := audit.Record(ctx, tx, audit.Change{Action: "ai.provider." + op, TargetType: "ai_provider", TargetID: after.ID,
		Actor: audit.UserActor(c.UserID), CorrelationID: c.CorrelationID, TenantID: c.TenantID, Before: b, After: a}); err != nil {
		return err
	}
	actor := c.UserID
	return events.Publish(ctx, tx, events.Publication{Type: "AIProviderChanged", ActorID: &actor, CorrelationID: c.CorrelationID,
		Payload: map[string]any{"providerId": after.ID, "operation": op, "enabled": after.Enabled}})
}

// ProviderTest is the outcome of a connection test: a code, never provider content.
type ProviderTest struct {
	OK         bool
	Code       string
	DurationMs int64
}

// TestProvider sends a fixed one-word prompt through the configured transport. Needs ai.settings.manage.
func (s *Service) TestProvider(ctx context.Context, c Caller, id string) (ProviderTest, error) {
	if err := s.requireAdmin(c, true); err != nil {
		return ProviderTest{}, err
	}
	if !s.cfg.Enabled {
		return ProviderTest{}, ErrDisabled // no provider call while the startup gate is off
	}
	if !uuidRE.MatchString(id) {
		return ProviderTest{}, ErrNotFound
	}
	rec, err := s.store.GetProvider(ctx, id)
	if err != nil {
		return ProviderTest{}, err
	}
	started := time.Now()
	res := ProviderTest{Code: "ok", OK: true}
	p, perr := s.providerFor(rec)
	if perr == nil {
		tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, perr = p.Chat(tctx, ChatRequest{System: "Connection test.", Messages: []Message{{Role: "user", Content: "Reply with OK."}}, MaxOutputTokens: 16})
		cancel()
	}
	if perr != nil {
		res.OK, res.Code = false, "provider_unavailable"
		s.cfg.Logger.Warn("ai provider test failed", "provider_id", id, "error", perr)
	}
	res.DurationMs = time.Since(started).Milliseconds()
	if err := s.record(ctx, c, "", "ai.provider.tested", "ai_provider", id, map[string]any{"outcome": res.Code, "durationMs": res.DurationMs}); err != nil {
		return ProviderTest{}, err
	}
	return res, nil
}

// ---- usage ----

// Usage returns aggregated usage of the last days. Needs ai.usage.view.
func (s *Service) Usage(ctx context.Context, c Caller, days int) ([]InstallationUsage, error) {
	if c.UserID == "" || c.TenantID == "" || !c.Has(PermUsageView) {
		return nil, ErrForbidden
	}
	if days < 1 || days > 90 {
		return nil, invalid("days must be between 1 and 90")
	}
	return s.store.UsageSummary(ctx, c.TenantID, days)
}

// ---- jobs ----

// Job types and schedules (registered by the worker; the jobs run even when the assistant is off, so retained
// data and sessions are always removed on time).
const (
	SessionsExpireJobType    = "ai.sessions.expire"
	SessionsExpireJobTimeout = time.Minute
	SessionsExpireInterval   = 5 * time.Minute
	RetentionPurgeJobType    = "ai.retention.purge"
	RetentionPurgeJobTimeout = 2 * time.Minute
	RetentionPurgeInterval   = 24 * time.Hour
	usageKeepDays            = 35
)

// HandleSessionsExpire deletes expired conversation sessions.
func (s *Service) HandleSessionsExpire(ctx context.Context, _ jobs.Job) error {
	_, err := s.store.ExpireSessions(ctx)
	return err
}

// HandleRetentionPurge deletes expired retained conversations and old usage rows and records one counts-only audit summary.
func (s *Service) HandleRetentionPurge(ctx context.Context, job jobs.Job) error {
	conv, msgs, usage, err := s.store.PurgeRetention(ctx, usageKeepDays)
	if err != nil {
		return fmt.Errorf("ai retention purge: %w", err)
	}
	if conv+msgs+usage == 0 {
		return nil
	}
	return pgx.BeginFunc(ctx, s.store.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Change{Action: "ai.retention.purged", TargetType: "ai_retention", TargetID: "job",
			Actor: audit.SystemActor("ai-retention"), CorrelationID: "job:" + job.ID,
			Metadata: map[string]any{"conversations": conv, "messages": msgs, "usageRows": usage}})
	})
}
