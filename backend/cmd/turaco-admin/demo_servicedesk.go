package main

import (
	"context"
	"fmt"

	knowledgeapp "github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/application"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// servicedesk seeds a few published help articles. Existing ones (same title) are kept.
func (d *demoSeeder) servicedesk(ctx context.Context) error {
	svc := wiring.Knowledge(d.e.pool)
	c := knowledgeapp.Caller{Actor: d.e.auditActor(), CorrelationID: "demo-seed"}
	p := knowledgeapp.Principal{UserID: "cli", Manage: true} // the CLI acts without a User; listing only needs a non-empty id
	existing, err := svc.List(ctx, p, "", "", knowledgeapp.Page{Limit: 200})
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, a := range existing.Items {
		have[a.Title] = true
	}
	for _, a := range []knowledgeapp.Input{
		{Title: "Connect to the guest Wi-Fi", Summary: "For visitors and private devices.", Audience: "employee",
			Body: "1. Open the Wi-Fi list and choose \"Guest\".\n2. Accept the terms in the page that opens.\n3. The connection works for 24 hours; repeat the steps afterwards."},
		{Title: "Reset your password", Summary: "If you forgot it or it expired.", Audience: "employee",
			Body: "Use the self-service page on the intranet or call the IT desk. Passwords expire every 180 days; choose at least 14 characters."},
		{Title: "Printer shows an error", Summary: "Quick checks before you report a problem.", Audience: "employee",
			Body: "Check paper and toner, turn the printer off and on, and make sure you picked the right printer. If it still fails, report a problem and name the printer."},
		{Title: "Escalating a lost device", Summary: "Internal checklist.", Audience: "internal",
			Body: "Mark the asset as lost, ask the user for the time and place, inform the security team, and revoke the device in the management console."},
	} {
		if have[a.Title] {
			continue
		}
		created, err := svc.Create(ctx, c, p, a)
		if err != nil {
			return fmt.Errorf("article %q: %w", a.Title, err)
		}
		if _, err := svc.Publish(ctx, c, p, created.ID, nil); err != nil {
			return fmt.Errorf("publish %q: %w", a.Title, err)
		}
	}
	return nil
}
