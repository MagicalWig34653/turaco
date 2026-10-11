package wiring

import (
	"context"
	"fmt"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/smtp"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/attachments"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

// SMTPProbe returns the read-only active probe of the mail relay (connect, EHLO, STARTTLS offered; no
// authentication, no mail), or nil when mail is not configured. Errors reading the password or CA file surface at
// startup like they do for the mailer.
func SMTPProbe(c config.SMTPConfig) (func(context.Context) error, error) {
	if !c.Enabled() {
		return nil, nil
	}
	mc, err := SMTPClientConfig(c)
	if err != nil {
		return nil, err
	}
	m, err := smtp.New(mc)
	if err != nil {
		return nil, fmt.Errorf("smtp probe: %w", err)
	}
	return func(ctx context.Context) error { return smtp.Probe(ctx, m) }, nil
}

// AttachmentProbes returns the read-only probes of the object storage (one bounded open of a stored object or of
// an id that cannot exist; nothing is written) and of the virus scanner (clamd PING), keyed by their health check.
// A nil service (storage not configured) has none.
func AttachmentProbes(svc *attachments.Service) map[string]func(context.Context) error {
	if svc == nil {
		return nil
	}
	return map[string]func(context.Context) error{
		"object_storage":     svc.ProbeStorage,
		"attachment_scanner": svc.PingScanner,
	}
}
