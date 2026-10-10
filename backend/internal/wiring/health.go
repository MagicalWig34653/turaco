package wiring

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	catalogpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/public"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepo "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	securitypublic "github.com/MagicalWig34653/turaco/backend/internal/modules/security/public"
	deskpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/health"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
)

// HealthConfig is the configuration the checks look at. Booleans and key names only: values never reach the
// health pages.
type HealthConfig struct {
	Environment, Version                                 string
	LDAPConfigured, KerberosConfigured                   bool
	SMTPConfigured, EmailBaseURLSet                      bool
	S3Configured                                         bool
	EmergencyLogin, LocalLogin                           bool
	IntuneSync, SoftwareSync, AutotaskSync, AdvisorySync bool
	RemoteAccessProviders                                []string
}

// Health builds the health registry: the platform checks plus the integration and module checks below. Every check
// reads stored facts or configuration; none calls the outside world.
func Health(pool *pgxpool.Pool, mods *modules.Service, cfg HealthConfig) (*health.Registry, error) {
	r := health.NewRegistry()
	if err := health.RegisterPlatformChecks(r, pool); err != nil {
		return nil, err
	}
	syncHealth := orgpublic.NewSyncHealth(orgrepo.New(pool))
	advisories := securitypublic.NewAdvisories(Security(pool))
	checks := []health.Check{
		{Key: "directory", Category: health.CategoryIntegration, Run: func(ctx context.Context) health.Result {
			if !cfg.LDAPConfigured {
				return notConfigured("LDAP_URL", "LDAP_BIND_DN", "LDAP_BIND_PASSWORD_FILE", "LDAP_USER_BASE_DN")
			}
			st, err := syncHealth.DirectorySyncStatus(ctx, orgpublic.SyncScope{})
			if err != nil {
				return health.Result{Status: health.StatusFailing, ErrorCode: "directory_status_unreadable"}
			}
			for _, s := range st {
				if s.LastFailureAt != nil && (s.LastSuccessAt == nil || s.LastFailureAt.After(*s.LastSuccessAt)) {
					code := s.LastErrorCode
					if code == "" {
						code = "directory_sync_failed"
					}
					return health.Result{Status: health.StatusFailing, Mode: health.ModeReal, ErrorCode: code, LastSuccessAt: s.LastSuccessAt, LastAttemptAt: s.LastFailureAt,
						NextStep: &health.NextStep{Kind: "route", Route: "/admin/directory"}}
				}
			}
			return health.Result{Status: health.StatusOK, Mode: health.ModeReal, NextStep: &health.NextStep{Kind: "route", Route: "/admin/directory"}}
		}},
		{Key: "kerberos", Category: health.CategoryIntegration, Run: func(context.Context) health.Result {
			if !cfg.KerberosConfigured {
				return notConfigured("KERBEROS_KEYTAB_FILE", "KERBEROS_SERVICE_PRINCIPAL", "KERBEROS_REALM")
			}
			return health.Result{Status: health.StatusUnknown, Mode: health.ModeReal, ErrorCode: "no_observation"}
		}},
		{Key: "smtp", Category: health.CategoryIntegration, Run: func(ctx context.Context) health.Result {
			if !cfg.SMTPConfigured {
				return notConfigured("SMTP_HOST", "SMTP_PORT", "SMTP_FROM")
			}
			if !cfg.EmailBaseURLSet {
				// Invitations, resets and links in mails cannot be built without it.
				return health.Result{Status: health.StatusNotConfigured, Mode: health.ModeReal, ErrorCode: "base_url_missing",
					NextStep: &health.NextStep{Kind: "config", ConfigKeys: []string{"EMAIL_BASE_URL"}}}
			}
			var ok, failed *time.Time
			var open int
			if err := pool.QueryRow(ctx, `SELECT max(delivered_at), max(updated_at) FILTER (WHERE status = 'failed'),
				count(*) FILTER (WHERE status IN ('pending','sending')) FROM platform.notification_deliveries WHERE channel = 'email'`).Scan(&ok, &failed, &open); err != nil {
				return health.Result{Status: health.StatusFailing, ErrorCode: "deliveries_unreadable"}
			}
			res := health.Result{Status: health.FreshStatus(time.Now(), 24*time.Hour, ok, failed), Mode: health.ModeReal, LastSuccessAt: ok, LastAttemptAt: failed, Counts: map[string]int{"open": open}}
			if res.Status == health.StatusStale {
				// Mail is event driven: no recent delivery is not a fault by itself.
				res.Status = health.StatusUnknown
			}
			if res.Status == health.StatusFailing {
				res.ErrorCode = "last_delivery_failed"
			}
			return res
		}},
		{Key: "object_storage", Category: health.CategoryIntegration, Run: func(context.Context) health.Result {
			if !cfg.S3Configured {
				return notConfigured("S3_ENDPOINT", "S3_BUCKET", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY")
			}
			return health.Result{Status: health.StatusUnknown, Mode: health.ModeReal, ErrorCode: "no_observation"}
		}},
		{Key: "emergency_login", Category: health.CategoryIntegration, Run: func(context.Context) health.Result {
			if cfg.EmergencyLogin {
				// Enabled break-glass login is an operating risk outside development.
				code := "emergency_login_enabled"
				if strings.EqualFold(cfg.Environment, "production") {
					return health.Result{Status: health.StatusFailing, ErrorCode: code, NextStep: &health.NextStep{Kind: "config", ConfigKeys: []string{"AUTH_EMERGENCY_LOGIN_ENABLED"}}}
				}
				return health.Result{Status: health.StatusOK, ErrorCode: code}
			}
			return health.Result{Status: health.StatusDisabled}
		}},
		providerCheck("intune", cfg.IntuneSync, "INTUNE_SYNC", "docs/integrations/intune.md"),
		providerCheck("software_provider", cfg.SoftwareSync, "SOFTWARE_PROVIDER_SYNC", "docs/integrations/intuneget.md"),
		providerCheck("autotask", cfg.AutotaskSync, "AUTOTASK_SYNC", "docs/integrations/autotask.md"),
		{Key: "advisory_feeds", Category: health.CategoryIntegration, Run: func(ctx context.Context) health.Result {
			if !cfg.AdvisorySync {
				return health.Result{Status: health.StatusDisabled, Mode: health.ModeReal, NextStep: &health.NextStep{Kind: "config", ConfigKeys: []string{"ADVISORY_SYNC"}, DocsPath: "docs/integrations/advisory-feeds.md"}}
			}
			feeds, err := advisories.AdvisoryFeedHealth(ctx)
			if err != nil {
				return health.Result{Status: health.StatusFailing, ErrorCode: "feeds_unreadable"}
			}
			if len(feeds) == 0 {
				return health.Result{Status: health.StatusUnknown, Mode: health.ModeReal, ErrorCode: "no_run_yet"}
			}
			res := health.Result{Status: health.StatusOK, Mode: health.ModeReal, Counts: map[string]int{"sources": len(feeds)}}
			for _, f := range feeds {
				if f.LastSuccessAt != nil && (res.LastSuccessAt == nil || f.LastSuccessAt.After(*res.LastSuccessAt)) {
					res.LastSuccessAt = f.LastSuccessAt
				}
				switch {
				case f.LastError != "":
					res.Status, res.ErrorCode = health.StatusFailing, "feed_failed"
				case f.Stale && res.Status == health.StatusOK:
					res.Status, res.ErrorCode = health.StatusStale, "feed_stale"
				}
			}
			return res
		}},
		{Key: "remote_access", Category: health.CategoryIntegration, Run: func(context.Context) health.Result {
			if len(cfg.RemoteAccessProviders) == 0 {
				return notConfigured("REMOTE_ACCESS_PROVIDERS")
			}
			// Launch-link connectors only; the unverified URI formats are documented in the remote access design.
			return health.Result{Status: health.StatusOK, Mode: health.ModeReal, Counts: map[string]int{"providers": len(cfg.RemoteAccessProviders)}}
		}},
	}
	for _, key := range []string{"presence", "ai"} {
		key := key
		checks = append(checks, health.Check{Key: key, Category: health.CategoryModule, Run: func(ctx context.Context) health.Result {
			return moduleResult(ctx, mods, key)
		}})
	}
	checks = append(checks, health.Check{Key: "modules", Category: health.CategoryModule, Run: func(ctx context.Context) health.Result {
		infos, err := mods.Status(ctx)
		if err != nil {
			return health.Result{Status: health.StatusFailing, ErrorCode: "modules_unreadable"}
		}
		blocked, off := 0, 0
		for _, i := range infos {
			switch i.State {
			case "blocked":
				blocked++
			case "disabled":
				off++
			}
		}
		res := health.Result{Status: health.StatusOK, Counts: map[string]int{"disabled": off, "blocked": blocked, "total": len(infos)}, NextStep: &health.NextStep{Kind: "route", Route: "/admin/modules"}}
		if blocked > 0 {
			res.Status, res.ErrorCode = health.StatusNotConfigured, "modules_blocked"
		}
		return res
	}})
	for _, c := range checks {
		if err := r.Register(c); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func notConfigured(keys ...string) health.Result {
	return health.Result{Status: health.StatusNotConfigured, Mode: health.ModeNotConfigured, NextStep: &health.NextStep{Kind: "config", ConfigKeys: keys}}
}

// providerCheck reports a provider integration. No real client exists for these providers yet (the ports have
// fake and "not configured" adapters only), so an enabled sync is still reported as not configured with the reason.
func providerCheck(key string, syncOn bool, flag, docs string) health.Check {
	return health.Check{Key: key, Category: health.CategoryIntegration, Run: func(context.Context) health.Result {
		step := &health.NextStep{Kind: "config", ConfigKeys: []string{flag}, DocsPath: docs}
		if !syncOn {
			return health.Result{Status: health.StatusDisabled, Mode: health.ModeNotConfigured, NextStep: step}
		}
		return health.Result{Status: health.StatusNotConfigured, Mode: health.ModeNotConfigured, ErrorCode: "client_not_built", NextStep: step}
	}}
}

func moduleResult(ctx context.Context, mods *modules.Service, key string) health.Result {
	infos, err := mods.Status(ctx)
	if err != nil {
		return health.Result{Status: health.StatusFailing, ErrorCode: "modules_unreadable"}
	}
	for _, i := range infos {
		if i.Module.Key != key {
			continue
		}
		step := &health.NextStep{Kind: "route", Route: "/admin/modules"}
		switch i.State {
		case "enabled":
			return health.Result{Status: health.StatusOK, NextStep: step}
		case "blocked":
			return health.Result{Status: health.StatusNotConfigured, ErrorCode: i.BlockedReason, NextStep: step}
		default:
			return health.Result{Status: health.StatusDisabled, NextStep: step}
		}
	}
	return health.Result{Status: health.StatusUnknown, ErrorCode: "module_unknown"}
}

// HealthSetup builds the setup checklist (F14): ten items derived from stored facts through public count
// contracts and the health registry; an administrator may skip an item or confirm the module defaults.
func HealthSetup(pool *pgxpool.Pool, mods *modules.Service, reg *health.Registry) *health.Setup {
	orgCounts := orgpublic.NewSetupCounts(pool)
	deskCounts := deskpublic.NewSetupCounts(pool)
	catalogCounts := catalogpublic.NewSetupCounts(pool)
	subjects := orgpublic.NewAuthorizationSubjects(orgrepo.New(pool))
	atLeast := func(f func(context.Context) (int, error), n int) func(context.Context) (bool, bool, error) {
		return func(ctx context.Context) (bool, bool, error) {
			c, err := f(ctx)
			return err == nil && c >= n, false, err
		}
	}
	checkOK := func(keys ...string) func(context.Context) (bool, bool, error) {
		return func(ctx context.Context) (bool, bool, error) {
			done, attention := true, false
			for _, k := range keys {
				e, ok := reg.Get(ctx, k)
				if !ok {
					continue
				}
				switch e.Status {
				case health.StatusOK, health.StatusDisabled:
				case health.StatusFailing, health.StatusStale:
					done, attention = false, true
				default:
					done = false
				}
			}
			return done, attention, nil
		}
	}
	items := []health.ItemDef{
		{Key: "administrators", Order: 1, Route: "/admin/people", Derive: func(ctx context.Context) (bool, bool, error) {
			rows, err := pool.Query(ctx, `SELECT a.subject_id FROM platform.role_assignments a JOIN platform.roles r ON r.id = a.role_id
				WHERE r.key = 'platform-administrator' AND r.deleted_at IS NULL AND a.subject_type = 'user' AND a.revoked_at IS NULL
				AND (a.expires_at IS NULL OR a.expires_at > now()) LIMIT 50`)
			if err != nil {
				return false, false, err
			}
			defer rows.Close()
			var ids []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return false, false, err
				}
				ids = append(ids, id)
			}
			if err := rows.Err(); err != nil {
				return false, false, err
			}
			if len(ids) == 0 {
				return false, true, nil
			}
			active, err := subjects.ActiveUsers(ctx, ids)
			if err != nil {
				return false, false, err
			}
			n := 0
			for _, a := range active {
				if a {
					n++
				}
			}
			return n >= 2, n == 1, nil
		}},
		{Key: "directory", Order: 2, Route: "/admin/directory", Derive: checkOK("directory")},
		{Key: "mail", Order: 3, Route: "/admin/health", Derive: checkOK("smtp")},
		{Key: "teams", Order: 4, Route: "/admin/teams", Derive: atLeast(orgCounts.TeamsWithMembers, 1)},
		{Key: "locations", Order: 5, Route: "/admin/locations", Derive: atLeast(orgCounts.ActiveSites, 1)},
		{Key: "queues", Order: 6, Route: "/service-desk/queues", Module: "servicedesk", Derive: atLeast(deskCounts.ActiveQueuesWithTeam, 1)},
		{Key: "roles", Order: 7, Route: "/admin/roles", Derive: func(ctx context.Context) (bool, bool, error) {
			var n int
			err := pool.QueryRow(ctx, `SELECT count(*) FROM platform.role_assignments a JOIN platform.roles r ON r.id = a.role_id
				WHERE NOT r.built_in AND r.deleted_at IS NULL AND a.revoked_at IS NULL AND (a.expires_at IS NULL OR a.expires_at > now())`).Scan(&n)
			return err == nil && n >= 1, false, err
		}},
		{Key: "catalog", Order: 8, Route: "/catalog", Module: "catalog", Derive: atLeast(catalogCounts.ActiveItems, 1)},
		{Key: "integrations", Order: 9, Route: "/admin/integrations", Derive: checkOK("intune", "software_provider", "autotask", "advisory_feeds", "remote_access")},
		{Key: "modules", Order: 10, Route: "/admin/modules", Confirmable: true},
	}
	return health.NewSetup(pool, items, func(ctx context.Context, key string) bool {
		ok, err := mods.Enabled(ctx, key)
		return err == nil && ok
	})
}
