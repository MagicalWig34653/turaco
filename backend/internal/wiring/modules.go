package wiring

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	presenceapp "github.com/MagicalWig34653/turaco/backend/internal/modules/presence/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
)

// Blocked reason codes of module preconditions (the UI translates "modules.blocked.<code>").
const (
	BlockedStartupGateOff       = "startup_gate_off"
	BlockedDPIANotRecorded      = "dpia_not_recorded"
	BlockedNoEnabledProvider    = "no_enabled_provider"
	BlockedNoProvidersConfigure = "no_providers_configured"
	// BlockedRuntimeSettingOff means the module's own audited runtime setting (presence.settings.enabled,
	// ai.settings.enabled) is off: the module refuses use, so the switch must not claim it is on.
	BlockedRuntimeSettingOff = "runtime_setting_off"
)

// ModuleGates is the startup configuration that the module switches must not bypass.
type ModuleGates struct {
	PresenceEnabled       bool     // PRESENCE_ENABLED
	AIEnabled             bool     // AI_ENABLED
	RemoteAccessProviders []string // REMOTE_ACCESS_PROVIDERS
}

// Modules builds the module registry with the preconditions of the modules that have their own gates: Workforce
// Presence (startup gate and the recorded impact assessment), Turaco AI (startup gate and an enabled provider) and
// Remote Access (configured providers). A switch is an additional layer on top of these, never a replacement.
func Modules(pool *pgxpool.Pool, gates ModuleGates) *modules.Service {
	presence := Presence(pool, presenceapp.Config{Enabled: gates.PresenceEnabled, RetentionDays: 30})
	aiStore := ai.NewStore(pool)
	return modules.NewService(pool, modules.Options{Preconditions: map[string]modules.Precondition{
		"presence": func(ctx context.Context) (string, error) {
			if !gates.PresenceEnabled {
				return BlockedStartupGateOff, nil
			}
			st, err := presence.Settings(ctx, presenceapp.Principal{Admin: true})
			if err != nil {
				return "", err
			}
			if st.DPIARecordedOn == nil {
				return BlockedDPIANotRecorded, nil
			}
			if !st.Enabled {
				return BlockedRuntimeSettingOff, nil
			}
			return "", nil
		},
		"ai": func(ctx context.Context) (string, error) {
			if !gates.AIEnabled {
				return BlockedStartupGateOff, nil
			}
			if _, err := aiStore.ActiveProvider(ctx); err != nil {
				if errors.Is(err, ai.ErrNotFound) {
					return BlockedNoEnabledProvider, nil
				}
				return "", err
			}
			st, err := aiStore.GetSettings(ctx)
			if err != nil {
				return "", err
			}
			if !st.Enabled {
				return BlockedRuntimeSettingOff, nil
			}
			return "", nil
		},
		"remoteaccess": func(context.Context) (string, error) {
			if len(gates.RemoteAccessProviders) == 0 {
				return BlockedNoProvidersConfigure, nil
			}
			return "", nil
		},
	}})
}
