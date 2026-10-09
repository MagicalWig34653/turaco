package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"context"

	changesapp "github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
	infraapp "github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/application"
	servicesapp "github.com/MagicalWig34653/turaco/backend/internal/modules/services/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// simVM is a virtual machine of the hospital data center (Configuration Item that services run on).
type simVM struct {
	Name, Address, Notes string
	VCPU, MemoryMB       int
}

var simVMs = []simVM{
	{"ORBIS-APP-01", "10.10.1.11", "ORBIS Anwendungsserver 1 (Standort Nord, Serverraum)", 8, 32768},
	{"ORBIS-APP-02", "10.10.1.12", "ORBIS Anwendungsserver 2 mit Medikationsdienst (ORBISMed)", 8, 32768},
	{"ORBIS-DB-01", "10.10.1.21", "ORBIS Datenbankserver", 16, 131072},
	{"PACS-01", "10.10.2.11", "PACS Bildarchiv Radiologie", 8, 65536},
	{"DC-01", "10.10.0.11", "Domänencontroller Nord", 4, 16384},
	{"DC-02", "10.10.0.12", "Domänencontroller Zentrale", 4, 16384},
	{"PRINT-01", "10.10.3.11", "Druckserver", 4, 8192},
	{"PBX-01", "10.10.4.11", "Telefonanlage (VoIP/DECT-Anbindung)", 4, 8192},
	{"WLC-01", "10.10.5.11", "WLAN-Controller", 4, 8192},
}

// simService is an IT service with its owner, the infrastructure it runs on and the services it needs. Assets are
// listed as product part number plus the numbers of the simulated units (see hospitalAssets).
type simService struct {
	Name, Description, Criticality string
	OwnerTeam, SupportTeam         string
	OwnerUser                      string
	VMs                            []string
	Assets                         []simAssetRef
	DependsOn                      []string
	// Status is set after creation when it is not operational; Reason is the status reason code.
	Status, Reason string
}

type simAssetRef struct {
	Part  string
	Units []int
}

var simServices = []simService{
	{Name: "Active Directory / Anmeldung", Description: "Zentrale Benutzerverwaltung und Anmeldung für Arbeitsplätze, Anwendungen und WLAN.", Criticality: "critical",
		OwnerTeam: teamInfra, SupportTeam: teamInfra, OwnerUser: "uwe.pohl", VMs: []string{"DC-01", "DC-02"}, Assets: []simAssetRef{{"SW-48", []int{1, 2}}}},
	{Name: "ORBIS/KIS", Description: "Krankenhausinformationssystem: Patientenakte, Medikation, Leistungsanforderung und Befunde.", Criticality: "critical",
		OwnerTeam: teamKIS, SupportTeam: teamInfra, OwnerUser: "henrik.vogel", VMs: []string{"ORBIS-APP-01", "ORBIS-APP-02", "ORBIS-DB-01"},
		Assets: []simAssetRef{{"SW-48", []int{1}}}, DependsOn: []string{"Active Directory / Anmeldung"}, Status: "degraded", Reason: "incident"},
	{Name: "WLAN", Description: "Medizin-WLAN, Mitarbeiter-WLAN und Gäste-WLAN an allen Standorten, inklusive Visitenwagen.", Criticality: "high",
		OwnerTeam: teamWLAN, SupportTeam: teamInfra, OwnerUser: "oliver.stein", VMs: []string{"WLC-01"}, Assets: []simAssetRef{{"AP-310", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}}},
		DependsOn: []string{"Active Directory / Anmeldung"}},
	{Name: "Telefonie (VoIP/DECT)", Description: "Festnetz, DECT-Stationstelefone und Rufumleitungen.", Criticality: "high",
		OwnerTeam: teamWLAN, SupportTeam: teamInfra, OwnerUser: "nadine.roth", VMs: []string{"PBX-01"}, Assets: []simAssetRef{{"SW-48", []int{1}}},
		DependsOn: []string{"WLAN"}},
	{Name: "PACS / Bildverteilung", Description: "Archiv und Verteilung von Röntgen-, CT- und MRT-Bildern für Radiologie und Stationen.", Criticality: "high",
		OwnerTeam: teamKIS, SupportTeam: teamInfra, OwnerUser: "tobias.kraft", VMs: []string{"PACS-01"}, Assets: []simAssetRef{{"DM-21", []int{1, 2, 3}}},
		DependsOn: []string{"Active Directory / Anmeldung"}},
	{Name: "Druckdienst", Description: "Netzwerkdrucker, Etiketten- und Armbanddrucker auf Stationen und in der Verwaltung.", Criticality: "medium",
		OwnerTeam: teamFLS, SupportTeam: teamInfra, OwnerUser: "lena.bauer", VMs: []string{"PRINT-01"},
		Assets:    []simAssetRef{{"LP-400", []int{1, 2, 3, 4, 5, 6}}, {"WB-10", []int{1, 2, 3}}},
		DependsOn: []string{"Active Directory / Anmeldung"}},
}

// simChange is a change with its window relative to the next Saturday evening. Steps are "submit", "assess" or
// "schedule"; Affected names services and VMs.
type simChange struct {
	Title, Description, Kind, Risk, Rollback string
	Requester, Owner, Assessor, Approver     string
	WeeksAhead, StartHour, Hours             int
	Services, VMs                            []string
	Steps                                    []string
}

var simChanges = []simChange{
	{Title: "ORBIS: Hotfix 2026.3.1 für das Medikationsmodul einspielen", Description: "Herstellerhotfix gegen die Zeitüberschreitungen im Medikationsmodul. Zuerst App-Server 2, danach App-Server 1.",
		Kind: "normal", Risk: "medium", Rollback: "Dienst ORBISMed stoppen, Hotfix deinstallieren, vorherigen Stand aus dem Snapshot wiederherstellen; Papier-Notfallprozess bleibt bereit.",
		Requester: "henrik.vogel", Owner: "sandra.winter", Assessor: "christian.hoffmann", Approver: "martin.kessler", WeeksAhead: 1, StartHour: 22, Hours: 4,
		Services: []string{"ORBIS/KIS"}, VMs: []string{"ORBIS-APP-01", "ORBIS-APP-02"}, Steps: []string{"submit", "assess"}},
	{Title: "WLAN: Firmware-Update der Access Points Klinik Süd", Description: "Neue Firmware gegen die Roaming-Abbrüche. Die Access Points werden nacheinander neu gestartet.",
		Kind: "normal", Risk: "low", Requester: "oliver.stein", Owner: "jens.albrecht", Assessor: "silke.brandl", WeeksAhead: 2, StartHour: 20, Hours: 3,
		Services: []string{"WLAN"}, VMs: []string{"WLC-01"}, Steps: []string{"submit"}},
	{Title: "Druckserver: Monatliche Windows-Updates einspielen", Description: "Monatliche Updates auf PRINT-01, Neustart außerhalb der Hauptdruckzeiten.",
		Kind: "standard", Risk: "low", Requester: "uwe.pohl", Owner: "mirja.engel", Assessor: "christian.hoffmann", WeeksAhead: 1, StartHour: 19, Hours: 2,
		Services: []string{"Druckdienst"}, VMs: []string{"PRINT-01"}, Steps: []string{"submit", "assess", "schedule"}},
}

// servicesAndChanges seeds IT services with dependencies on infrastructure and a few changes with maintenance
// windows. Everything goes through the audited application operations and is idempotent: existing services,
// virtual machines, dependencies and changes (by name or title) are reused.
func (h *hospitalSeeder) servicesAndChanges(ctx context.Context) error {
	vmIDs, err := h.virtualMachines(ctx)
	if err != nil {
		return fmt.Errorf("virtual machines: %w", err)
	}
	svcIDs, err := h.itServices(ctx, vmIDs)
	if err != nil {
		return fmt.Errorf("services: %w", err)
	}
	if err := h.maintenanceChanges(ctx, svcIDs, vmIDs); err != nil {
		return fmt.Errorf("changes: %w", err)
	}
	return nil
}

func (h *hospitalSeeder) virtualMachines(ctx context.Context) (map[string]string, error) {
	svc := wiring.Infrastructure(h.e.pool)
	c := infraapp.Caller{Actor: h.e.auditActor(), CorrelationID: hospitalCorrelation}
	p := infraapp.Principal{Manage: true, View: true}
	out := map[string]string{}
	for _, v := range simVMs {
		found, err := svc.ListVMs(ctx, p, infraapp.VMFilter{Query: v.Name, Page: infraapp.Page{Limit: 50}})
		if err != nil {
			return nil, err
		}
		for _, x := range found.Items {
			if strings.EqualFold(x.Name, v.Name) {
				out[v.Name] = x.ID
			}
		}
		if _, ok := out[v.Name]; ok {
			continue
		}
		vm, err := svc.CreateVM(ctx, c, p, infraapp.VMInput{Name: v.Name, State: infraapp.VMRunning, VCPU: v.VCPU, MemoryMB: v.MemoryMB, ManagementAddress: v.Address, Notes: v.Notes})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", v.Name, err)
		}
		out[v.Name] = vm.ID
		h.count("virtual machines")
	}
	return out, nil
}

func (h *hospitalSeeder) itServices(ctx context.Context, vmIDs map[string]string) (map[string]string, error) {
	svc := wiring.Services(h.e.pool)
	c := servicesapp.Caller{Actor: h.e.auditActor(), CorrelationID: hospitalCorrelation}
	p := servicesapp.Principal{View: true, Manage: true, InfraView: true, AssetsView: true}
	ids := map[string]string{}
	created := map[string]servicesapp.Service{}
	for _, s := range simServices {
		found, err := svc.List(ctx, p, servicesapp.Filter{Query: s.Name, IncludeRetired: false, Page: servicesapp.Page{Limit: 50}})
		if err != nil {
			return nil, err
		}
		for _, x := range found.Items {
			if strings.EqualFold(x.Name, s.Name) {
				ids[s.Name] = x.ID
			}
		}
		if _, ok := ids[s.Name]; ok {
			continue
		}
		v, err := svc.Create(ctx, c, p, servicesapp.Input{Name: s.Name, Description: s.Description, Criticality: s.Criticality,
			OwnerUserID: h.users[s.OwnerUser], OwnerTeamID: h.teams[s.OwnerTeam], SupportTeamID: h.teams[s.SupportTeam]})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.Name, err)
		}
		ids[s.Name], created[s.Name] = v.ID, v
		h.count("services")
	}
	// Dependencies: services first (so an order in the table does not matter), then virtual machines and assets.
	for _, s := range simServices {
		id := ids[s.Name]
		for _, dep := range s.DependsOn {
			if err := h.dependency(ctx, svc, c, p, id, "service", ids[dep]); err != nil {
				return nil, fmt.Errorf("%s -> %s: %w", s.Name, dep, err)
			}
		}
		for _, vm := range s.VMs {
			if err := h.dependency(ctx, svc, c, p, id, "vm", vmIDs[vm]); err != nil {
				return nil, fmt.Errorf("%s -> %s: %w", s.Name, vm, err)
			}
		}
		for _, ref := range s.Assets {
			for _, n := range ref.Units {
				serial := fmt.Sprintf("SIM-%s-%03d", ref.Part, n)
				assetID, ok := h.assets[serial]
				if !ok {
					return nil, fmt.Errorf("%s: unknown asset %s", s.Name, serial)
				}
				if err := h.dependency(ctx, svc, c, p, id, "asset", assetID); err != nil {
					return nil, fmt.Errorf("%s -> %s: %w", s.Name, serial, err)
				}
			}
		}
	}
	// A new service in a degraded state shows the current disruption on the overview.
	for _, s := range simServices {
		v, ok := created[s.Name]
		if !ok || s.Status == "" {
			continue
		}
		if _, err := svc.ChangeStatus(ctx, c, p, v.ID, &v.Version, s.Status, s.Reason); err != nil {
			return nil, fmt.Errorf("%s status: %w", s.Name, err)
		}
	}
	return ids, nil
}

func (h *hospitalSeeder) dependency(ctx context.Context, svc *servicesapp.App, c servicesapp.Caller, p servicesapp.Principal, serviceID, targetType, targetID string) error {
	_, created, err := svc.AddDependency(ctx, c, p, serviceID, targetType, targetID)
	if err != nil {
		return err
	}
	if created {
		h.count("service dependencies")
	}
	return nil
}

// nextSaturday returns the Saturday weeksAhead weeks after the coming Saturday at the given hour, in the
// hospital's time zone, so the windows are always in the future when the seed runs.
func nextSaturday(now time.Time, weeksAhead, hour int) time.Time {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		loc = time.UTC
	}
	n := now.In(loc)
	days := (int(time.Saturday) - int(n.Weekday()) + 7) % 7
	if days == 0 {
		days = 7
	}
	d := n.AddDate(0, 0, days+7*weeksAhead)
	return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, loc).UTC()
}

func (h *hospitalSeeder) maintenanceChanges(ctx context.Context, svcIDs, vmIDs map[string]string) error {
	svc := wiring.Changes(h.e.pool)
	principal := func(login string) changesapp.Principal {
		return changesapp.Principal{UserID: h.users[login], View: true, Manage: true, ServicesView: true, InfraView: true, AssetsView: true}
	}
	caller := func(login string) changesapp.Caller {
		return changesapp.Caller{Actor: audit.UserActor(h.users[login]), CorrelationID: hospitalCorrelation}
	}
	for _, ch := range simChanges {
		reader := principal(ch.Requester)
		existing, err := svc.List(ctx, reader, changesapp.Filter{RequesterID: h.users[ch.Requester], Page: changesapp.Page{Limit: 100}})
		if err != nil {
			return err
		}
		found := false
		for _, x := range existing.Items {
			found = found || x.Title == ch.Title
		}
		if found {
			continue
		}
		start := nextSaturday(time.Now(), ch.WeeksAhead, ch.StartHour)
		end := start.Add(time.Duration(ch.Hours) * time.Hour)
		c, err := svc.Create(ctx, caller(ch.Requester), reader, changesapp.NewChange{Title: ch.Title, Description: ch.Description, Kind: ch.Kind, Risk: ch.Risk,
			OwnerUserID: h.users[ch.Owner], RollbackPlan: ch.Rollback, Window: changesapp.Window{Start: &start, End: &end}})
		if err != nil {
			return fmt.Errorf("%q: %w", ch.Title, err)
		}
		for _, name := range ch.Services {
			if _, _, err := svc.AddAffected(ctx, caller(ch.Requester), reader, c.ID, nil, "service", svcIDs[name]); err != nil {
				return fmt.Errorf("%q affects %s: %w", ch.Title, name, err)
			}
		}
		for _, name := range ch.VMs {
			if _, _, err := svc.AddAffected(ctx, caller(ch.Requester), reader, c.ID, nil, "vm", vmIDs[name]); err != nil {
				return fmt.Errorf("%q affects %s: %w", ch.Title, name, err)
			}
		}
		h.count("changes")
		version := func(p changesapp.Principal) (*int, error) {
			d, err := svc.Get(ctx, p, c.ID)
			if err != nil {
				return nil, err
			}
			v := d.Change.Version
			return &v, nil
		}
		for _, step := range ch.Steps {
			v, err := version(reader)
			if err != nil {
				return err
			}
			switch step {
			case "submit":
				_, err = svc.Submit(ctx, caller(ch.Requester), reader, c.ID, v)
			case "assess":
				a := changesapp.Assessment{Risk: ch.Risk}
				if ch.Approver != "" {
					a.Approver = changesapp.Approver{UserID: ptr(h.users[ch.Approver])}
				}
				_, err = svc.Assess(ctx, caller(ch.Assessor), principal(ch.Assessor), c.ID, v, a)
			case "schedule":
				_, err = svc.Schedule(ctx, caller(ch.Assessor), principal(ch.Assessor), c.ID, v, changesapp.ScheduleInput{})
			default:
				err = errors.New("unknown step " + step)
			}
			if err != nil {
				return fmt.Errorf("%q %s: %w", ch.Title, step, err)
			}
		}
	}
	return nil
}
