package wiring

import (
	"fmt"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/autotask"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/intune"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/microsoft"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/providerstatus"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

// ProviderHealth describes what the health check of a provider integration may report. Real clients are written
// from the vendor documentation and stay "unverified" until a call was observed to succeed; this wiring never
// claims more.
type ProviderHealth struct {
	// Built is true when a real client exists for the provider at all (false: only fake and placeholder adapters).
	Built bool
	// Configured is true when the real client was built from configuration in this process.
	Configured bool
	// MissingKeys names configuration keys to set when it is not configured.
	MissingKeys []string
	// Reporter observes the client in this process; nil when not configured.
	Reporter providerstatus.Reporter
	// CredentialExpires is the credential's expiry when known.
	CredentialExpires *time.Time
}

func graphClient(reg config.GraphRegistration) (*microsoft.Graph, microsoft.Credential, error) {
	var cred microsoft.Credential
	var err error
	if reg.SecretFile != "" {
		cred, err = microsoft.NewSecretCredential(reg.SecretFile, reg.SecretExpiresAt)
	} else {
		cred, err = microsoft.NewCertificateCredential(reg.CertificateFile, reg.PrivateKeyFile)
	}
	if err != nil {
		return nil, nil, err
	}
	g, err := microsoft.NewGraph(microsoft.GraphConfig{TenantID: reg.TenantID, ClientID: reg.ClientID, Credential: cred, HTTPProxy: reg.HTTPProxy, CAFile: reg.CAFile})
	if err != nil {
		return nil, nil, err
	}
	return g, cred, nil
}

var graphReadKeys = []string{"MICROSOFT_GRAPH_TENANT_ID", "MICROSOFT_GRAPH_CLIENT_ID", "MICROSOFT_GRAPH_CLIENT_SECRET_FILE", "MICROSOFT_GRAPH_CLIENT_CERTIFICATE_FILE"}

// IntuneProvider returns the Intune read provider: the Graph client when the read registration is configured,
// otherwise the "not configured" placeholder. beta allows the Graph beta endpoints (INTUNE_GRAPH_BETA).
func IntuneProvider(reg config.GraphRegistration, beta bool) (intune.Provider, ProviderHealth, error) {
	if !reg.Configured {
		return intune.NotConfigured{}, ProviderHealth{Built: true, MissingKeys: graphReadKeys}, nil
	}
	g, cred, err := graphClient(reg)
	if err != nil {
		return nil, ProviderHealth{}, fmt.Errorf("configure the Intune Graph read client: %w", err)
	}
	h := ProviderHealth{Built: true, Configured: true, Reporter: g}
	if exp, ok := cred.ExpiresAt(); ok {
		h.CredentialExpires = &exp
	}
	return intune.NewGraphProvider(g, beta), h, nil
}

// IntuneWriter returns the Management Assignment Writer: the Graph write client when the write registration is
// configured, otherwise the placeholder whose writes fail permanently. Only the worker's deployment engine writes.
func IntuneWriter(reg config.GraphRegistration) (intune.AssignmentWriter, ProviderHealth, error) {
	if !reg.Configured {
		return intune.NotConfiguredWriter{}, ProviderHealth{Built: true, MissingKeys: []string{"MICROSOFT_GRAPH_WRITE_TENANT_ID", "MICROSOFT_GRAPH_WRITE_CLIENT_ID", "MICROSOFT_GRAPH_WRITE_CLIENT_SECRET_FILE"}}, nil
	}
	g, cred, err := graphClient(reg)
	if err != nil {
		return nil, ProviderHealth{}, fmt.Errorf("configure the Intune Graph write client: %w", err)
	}
	h := ProviderHealth{Built: true, Configured: true, Reporter: g}
	if exp, ok := cred.ExpiresAt(); ok {
		h.CredentialExpires = &exp
	}
	return intune.NewGraphWriter(g), h, nil
}

// AutotaskGateway returns the Autotask gateway: the REST client when the API user is configured, otherwise the
// "not configured" placeholder.
func AutotaskGateway(c config.AutotaskCredentials) (autotask.Gateway, ProviderHealth, error) {
	if !c.Configured {
		return autotask.NotConfigured{}, ProviderHealth{Built: true, MissingKeys: []string{"AUTOTASK_API_USERNAME", "AUTOTASK_API_SECRET_FILE", "AUTOTASK_INTEGRATION_CODE", "AUTOTASK_COMPANY_ID", "AUTOTASK_STATUS_MAP", "AUTOTASK_PRIORITY_MAP"}}, nil
	}
	rest, err := autotask.NewREST(autotask.RESTConfig{
		Username: c.Username, SecretFile: c.SecretFile, IntegrationCode: c.IntegrationCode, CompanyID: c.CompanyID, QueueID: c.QueueID,
		StatusMap: c.StatusMap, PriorityMap: c.PriorityMap, RatePerHour: c.RateLimitPerHour,
	})
	if err != nil {
		return nil, ProviderHealth{}, fmt.Errorf("configure the Autotask REST client: %w", err)
	}
	return rest, ProviderHealth{Built: true, Configured: true, Reporter: rest}, nil
}
