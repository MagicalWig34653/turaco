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
	// Earlier seed runs created these articles in English; they are retired when the German ones are created.
	legacy := map[string]string{"Mit dem Gäste-WLAN verbinden": "Connect to the guest Wi-Fi", "Passwort zurücksetzen": "Reset your password",
		"Drucker zeigt einen Fehler": "Printer shows an error", "Verlorenes Gerät eskalieren": "Escalating a lost device"}
	old := map[string]knowledgeapp.Article{}
	for _, a := range existing.Items {
		have[a.Title] = true
		old[a.Title] = a
	}
	for _, a := range []knowledgeapp.Input{
		{Title: "Mit dem Gäste-WLAN verbinden", Summary: "Für Besucher und private Geräte.", Audience: "employee",
			Body: "1. Öffnen Sie die WLAN-Liste und wählen Sie \"Gast\".\n2. Akzeptieren Sie die Nutzungsbedingungen auf der Seite, die sich öffnet.\n3. Die Verbindung gilt 24 Stunden; wiederholen Sie die Schritte danach."},
		{Title: "Passwort zurücksetzen", Summary: "Wenn Sie es vergessen haben oder es abgelaufen ist.", Audience: "employee",
			Body: "Nutzen Sie die Selbstbedienungsseite im Intranet oder rufen Sie den IT-Support an. Passwörter laufen alle 180 Tage ab; wählen Sie mindestens 14 Zeichen."},
		{Title: "Drucker zeigt einen Fehler", Summary: "Kurze Prüfungen, bevor Sie ein Problem melden.", Audience: "employee",
			Body: "Prüfen Sie Papier und Toner, schalten Sie den Drucker aus und wieder ein und achten Sie darauf, dass der richtige Drucker gewählt ist. Hilft das nicht, melden Sie ein Problem und nennen Sie den Drucker."},
		{Title: "Verlorenes Gerät eskalieren", Summary: "Interne Checkliste.", Audience: "internal",
			Body: "Das Gerät als verloren markieren, Zeit und Ort bei der Nutzerin oder dem Nutzer erfragen, das Security-Team informieren und das Gerät in der Verwaltungskonsole sperren."},
	} {
		if have[a.Title] {
			continue
		}
		if prev, ok := old[legacy[a.Title]]; ok && prev.Status == "published" {
			if _, err := svc.Retire(ctx, c, p, prev.ID, nil); err != nil {
				return fmt.Errorf("retire %q: %w", prev.Title, err)
			}
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
