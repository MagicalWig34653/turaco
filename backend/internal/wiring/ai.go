package wiring

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/softwaremgmt"
	endpointspublic "github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/public"
	knowledgepublic "github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/public"
	servicedeskpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai/providers/fake"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai/providers/openaicompat"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

// AIConfig is the installation configuration of Turaco AI.
type AIConfig struct {
	Enabled   bool
	TenantID  string
	SecretDir string
	Logger    *slog.Logger
}

// AIProviderFactory builds provider adapters. Credentials are read from the deployment secret file the provider's
// secretRef names; they never touch the database, logs or prompts.
func AIProviderFactory(secretDir string) ai.ProviderFactory {
	return func(rec ai.ProviderRecord) (ai.Provider, error) {
		switch rec.Kind {
		case "fake":
			return fake.Deterministic(), nil
		case "openai_compatible":
			key := ""
			if rec.SecretRef != nil {
				if secretDir == "" {
					return nil, errors.New("AI_SECRET_DIR is not set")
				}
				var err error
				if key, err = config.ReadSecretFile(filepath.Join(secretDir, filepath.Base(*rec.SecretRef))); err != nil {
					return nil, fmt.Errorf("provider secret: %w", err)
				}
			}
			return openaicompat.New(openaicompat.Config{Endpoint: rec.EndpointURL, Model: rec.Model, APIKey: key, Local: rec.Local})
		}
		return nil, fmt.Errorf("unknown provider kind %q", rec.Kind)
	}
}

// AI builds the AI service with the tools that modules contribute through their public contracts. A tool that
// fails the registry self-check stops startup.
func AI(pool *pgxpool.Pool, cfg AIConfig) (*ai.Service, error) {
	reg := ai.NewRegistry()
	if cfg.Enabled {
		endpoints := Endpoints(pool, intune.NotConfigured{}, false, softwaremgmt.NotConfigured{}, false)
		var tools []ai.Tool
		tools = append(tools, servicedeskpublic.AITools(ServiceDesk(pool))...)
		tools = append(tools, knowledgepublic.AITools(Knowledge(pool))...)
		tools = append(tools, endpointspublic.AITools(endpoints)...)
		if err := reg.RegisterAll(tools...); err != nil {
			return nil, err
		}
	}
	return ai.NewService(ai.NewStore(pool), reg, ai.Config{Enabled: cfg.Enabled, TenantID: cfg.TenantID,
		Factory: AIProviderFactory(cfg.SecretDir), Logger: cfg.Logger}), nil
}
