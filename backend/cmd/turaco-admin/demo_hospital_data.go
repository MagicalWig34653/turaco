package main

import "fmt"

// Static data of the hospital simulation (docs/development/simulation-hospital.md).
// Names, texts and tickets are German on purpose: they are test data for German-speaking usability
// testers, not UI strings. Keep the persona tables of the document in sync with simPeople (a test checks
// that every login is documented).

// hospitalPassword is the shared, public development password of every simulation login. The emergency
// credential policy requires at least 16 characters.
const hospitalPassword = "turaco-sim-password"

type simSite struct{ Key, Name string }

var simSites = []simSite{
	{"nord", "Universitätsklinikum Nord (Hauptcampus)"},
	{"sued", "Klinik Süd"},
	{"zentrale", "Verwaltung / Zentrale"},
}

// simAreas are Locations below a site. Locations are flat in Organization (no parent), so the site is
// part of the external key and the name.
type simArea struct{ Key, Site, Name string }

var simAreas = []simArea{
	{"nord-notaufnahme", "nord", "Nord – Notaufnahme"},
	{"nord-radiologie", "nord", "Nord – Radiologie"},
	{"nord-station3b", "nord", "Nord – Station 3B"},
	{"nord-pflege", "nord", "Nord – Pflegedienstleitung"},
	{"nord-labor", "nord", "Nord – Labor"},
	{"nord-serverraum", "nord", "Nord – Serverraum Haus A"},
	{"sued-chirurgie", "sued", "Süd – Chirurgie"},
	{"sued-pflege", "sued", "Süd – Pflege"},
	{"sued-technik", "sued", "Süd – Technikraum"},
	{"zentrale-hr", "zentrale", "Zentrale – Personalabteilung"},
	{"zentrale-einkauf", "zentrale", "Zentrale – Einkauf"},
	{"zentrale-buchhaltung", "zentrale", "Zentrale – Buchhaltung"},
	{"zentrale-rz", "zentrale", "Zentrale – Rechenzentrum"},
}

type simDepartment struct{ Key, Name string }

var simDepartments = []simDepartment{
	{"notaufnahme", "Notaufnahme"}, {"radiologie", "Radiologie"}, {"station3b", "Station 3B"}, {"pflege", "Pflege"},
	{"labor", "Labor"}, {"chirurgie", "Chirurgie"}, {"hr", "Personalabteilung"}, {"einkauf", "Einkauf"},
	{"buchhaltung", "Buchhaltung"}, {"it", "IT"}, {"vendor", "Externer Dienstleister"},
}

// Team keys.
const (
	teamFLS    = "fls"
	teamWLAN   = "wlan"
	teamKIS    = "kis"
	teamInfra  = "infra"
	teamSec    = "sec"
	teamLeads  = "leads"
	teamVendor = "vendor"
)

type simTeam struct{ Key, Name string }

var simTeams = []simTeam{
	{teamFLS, "First Level Support"},
	{teamWLAN, "Telefonie & WLAN"},
	{teamKIS, "ORBIS/KIS"},
	{teamInfra, "Infrastruktur"},
	{teamSec, "Security"},
	{teamLeads, "Standort-IT-Leitung"},
	{teamVendor, "Dienstleister KIS-Hersteller"},
}

// Role keys.
const (
	roleFirstLevel = "first-level-support"
	roleSpecialist = "it-specialist"
	roleSiteLead   = "it-site-lead"
	roleSecurity   = "security-analyst"
	roleInfra      = "infrastructure-engineer"
	roleVendor     = "vendor-restricted"
)

type simRole struct {
	Key, Name, Description string
	Permissions            []string
}

var simRoles = []simRole{
	{roleFirstLevel, "First-level support", "Simulation: triage and work tickets, read knowledge, assets and devices; no changes to assets or knowledge.",
		[]string{"tickets.manage", "knowledge.view", "assets.view", "organization.view", "tasks.work", "briefing.view", "endpoints.view",
			"remote_access.view", "remote_access.start_attended", "runbooks.execute", "requests.view", "services.view", "presence.manage_own", "views.share"}},
	{roleSpecialist, "IT specialist", "Simulation: second-level technician (WLAN, ORBIS/KIS): first level plus assets, knowledge, problems and tasks.",
		[]string{"tickets.manage", "knowledge.view", "knowledge.manage", "assets.view", "assets.manage", "organization.view", "tasks.work", "tasks.manage",
			"briefing.view", "endpoints.view", "endpoint.management.view", "remote_access.view", "remote_access.start_attended", "runbooks.execute",
			"requests.view", "services.view", "problems.manage", "changes.view", "infrastructure.view", "security.view", "products.view",
			"inventory.view", "software.view", "deployments.view", "presence.manage_own", "views.share"}},
	{roleSiteLead, "IT site lead", "Simulation: Standort-IT-Leiter: specialist plus briefing, major incidents, changes, teams and presence of the team.",
		[]string{"tickets.manage", "knowledge.view", "knowledge.manage", "assets.view", "assets.manage", "organization.view", "organization.teams.manage",
			"tasks.work", "tasks.manage", "briefing.view", "briefing.manage", "endpoints.view", "endpoint.management.view", "remote_access.view",
			"remote_access.start_attended", "runbooks.execute", "requests.view", "requests.manage", "services.view", "problems.manage",
			"majorincidents.manage", "changes.view", "changes.manage", "changes.approve", "planning.view", "infrastructure.view", "security.view",
			"products.view", "inventory.view", "procurement.view", "software.view", "deployments.view", "presence.manage_own",
			"presence.view_availability", "presence.manage_entries", "views.share"}},
	{roleSecurity, "Security analyst", "Simulation: security advisories, findings and risk acceptance; reads tickets, devices and the audit log without working tickets.",
		[]string{"security.manage", "security.view", "security.accept_risk", "tickets.view", "knowledge.view", "knowledge.manage", "assets.view",
			"endpoints.view", "endpoint.management.view", "organization.view", "organization.directory.view", "tasks.work", "briefing.view",
			"changes.view", "services.view", "infrastructure.view", "software.view", "platform.audit.view", "remote_access.view_sessions",
			"presence.manage_own"}},
	{roleInfra, "Infrastructure engineer", "Simulation: racks, rooms, virtual machines, services and changes; sees tickets but works tasks and changes.",
		[]string{"infrastructure.manage", "infrastructure.view", "changes.manage", "changes.execute", "changes.view", "services.manage", "services.view",
			"assets.manage", "assets.view", "endpoints.view", "tasks.work", "tasks.view", "knowledge.view", "knowledge.manage", "tickets.view",
			"briefing.view", "organization.view", "planning.view", "inventory.view", "procurement.view", "software.view", "problems.manage",
			"presence.manage_own"}},
	{roleVendor, "Vendor (restricted)", "Simulation: external KIS vendor. Only tasks assigned to the vendor team or to the person; every signed-in user can additionally raise and read own tickets.",
		[]string{"tasks.work"}},
}

// simQueue is a ticket desk of the simulation: one per specialist Team, with that Team working it. The default
// Queue created by the platform ("it", prefix TKT) stays the intake desk of First Level Support and is renamed in
// the seed; tickets of the First Level, site lead and vendor Teams stay in it.
type simQueue struct {
	Key, Prefix, Name, Label, Team string
}

var simQueues = []simQueue{
	{"telefonie-wlan", "TEL", "Telefonie & WLAN", "Telefonie & WLAN", teamWLAN},
	{"orbis-kis", "KIS", "ORBIS/KIS", "ORBIS/KIS", teamKIS},
	{"infrastruktur", "INF", "Infrastruktur", "Infrastruktur", teamInfra},
	{"security", "SEC", "Security", "Security", teamSec},
}

// simIntakeQueueName is the name the seed gives the platform's default Queue.
const simIntakeQueueName = "Allgemein (First Level)"

// queueOfTeam returns the simQueue a routing Team works, if any.
func queueOfTeam(team string) (simQueue, bool) {
	for _, q := range simQueues {
		if q.Team == team {
			return q, true
		}
	}
	return simQueue{}, false
}

type simMembership struct{ Team, Role string }

// simPerson is a simulated user. Manager is the login of the manager; Role the role key ("" for the implicit
// employee baseline, which every signed-in user has).
type simPerson struct {
	Login, Prefix, Given, Family string
	Site, Area, Dept             string
	Manager                      string
	Role                         string
	Teams                        []simMembership
	Persona                      string
}

func (p simPerson) displayName() string {
	n := p.Given + " " + p.Family
	if p.Prefix != "" {
		n = p.Prefix + " " + n
	}
	return n
}

func (p simPerson) email() string {
	switch p.Site {
	case "sued":
		return p.Login + "@klinik-sued.example"
	case "zentrale":
		return p.Login + "@verwaltung-nord.example"
	}
	if p.Dept == "vendor" {
		return p.Login + "@kis-hersteller.example"
	}
	return p.Login + "@uk-nord.example"
}

var simPeople = []simPerson{
	// Standort-IT-Leiter (one per site).
	{Login: "christian.hoffmann", Given: "Christian", Family: "Hoffmann", Site: "nord", Area: "nord-serverraum", Dept: "it", Role: roleSiteLead,
		Teams: []simMembership{{teamLeads, "Standort-IT-Leiter Nord"}}, Persona: "IT lead Nord (08:00 stand-up host)"},
	{Login: "silke.brandl", Given: "Silke", Family: "Brandl", Site: "sued", Area: "sued-technik", Dept: "it", Role: roleSiteLead,
		Teams: []simMembership{{teamLeads, "Standort-IT-Leiter Süd"}}, Persona: "IT lead Süd"},
	{Login: "martin.kessler", Given: "Martin", Family: "Kessler", Site: "zentrale", Area: "zentrale-rz", Dept: "it", Role: roleSiteLead,
		Teams: []simMembership{{teamLeads, "Standort-IT-Leiter Zentrale"}}, Persona: "IT lead Zentrale"},
	// First Level Support.
	{Login: "lena.bauer", Given: "Lena", Family: "Bauer", Site: "nord", Area: "nord-serverraum", Dept: "it", Manager: "christian.hoffmann", Role: roleFirstLevel,
		Teams: []simMembership{{teamFLS, "Teamleitung"}}, Persona: "First-level triage"},
	{Login: "murat.demir", Given: "Murat", Family: "Demir", Site: "sued", Area: "sued-technik", Dept: "it", Manager: "silke.brandl", Role: roleFirstLevel,
		Teams: []simMembership{{teamFLS, ""}}, Persona: "First level Süd"},
	// Telefonie & WLAN.
	{Login: "oliver.stein", Given: "Oliver", Family: "Stein", Site: "nord", Area: "nord-serverraum", Dept: "it", Manager: "christian.hoffmann", Role: roleSpecialist,
		Teams: []simMembership{{teamWLAN, "Teamleitung"}}, Persona: "WLAN technician"},
	{Login: "nadine.roth", Given: "Nadine", Family: "Roth", Site: "nord", Area: "nord-serverraum", Dept: "it", Manager: "christian.hoffmann", Role: roleSpecialist,
		Teams: []simMembership{{teamWLAN, ""}}, Persona: "Telephony technician"},
	{Login: "jens.albrecht", Given: "Jens", Family: "Albrecht", Site: "sued", Area: "sued-technik", Dept: "it", Manager: "silke.brandl", Role: roleSpecialist,
		Teams: []simMembership{{teamWLAN, ""}}, Persona: "WLAN technician Süd"},
	// ORBIS/KIS.
	{Login: "henrik.vogel", Given: "Henrik", Family: "Vogel", Site: "nord", Area: "nord-serverraum", Dept: "it", Manager: "christian.hoffmann", Role: roleSpecialist,
		Teams: []simMembership{{teamKIS, "Teamleitung"}}, Persona: "ORBIS technician"},
	{Login: "sandra.winter", Given: "Sandra", Family: "Winter", Site: "nord", Area: "nord-serverraum", Dept: "it", Manager: "christian.hoffmann", Role: roleSpecialist,
		Teams: []simMembership{{teamKIS, ""}}, Persona: "ORBIS technician"},
	{Login: "tobias.kraft", Given: "Tobias", Family: "Kraft", Site: "zentrale", Area: "zentrale-rz", Dept: "it", Manager: "martin.kessler", Role: roleSpecialist,
		Teams: []simMembership{{teamKIS, ""}}, Persona: "ORBIS technician Zentrale"},
	// Infrastruktur.
	{Login: "uwe.pohl", Given: "Uwe", Family: "Pohl", Site: "nord", Area: "nord-serverraum", Dept: "it", Manager: "christian.hoffmann", Role: roleInfra,
		Teams: []simMembership{{teamInfra, "Teamleitung"}}, Persona: "Infrastructure specialist"},
	{Login: "mirja.engel", Given: "Mirja", Family: "Engel", Site: "sued", Area: "sued-technik", Dept: "it", Manager: "silke.brandl", Role: roleInfra,
		Teams: []simMembership{{teamInfra, ""}}, Persona: "Infrastructure specialist Süd"},
	// Security.
	{Login: "ines.falk", Given: "Ines", Family: "Falk", Site: "zentrale", Area: "zentrale-rz", Dept: "it", Manager: "martin.kessler", Role: roleSecurity,
		Teams: []simMembership{{teamSec, "Teamleitung"}}, Persona: "Security analyst"},
	{Login: "deniz.arslan", Given: "Deniz", Family: "Arslan", Site: "nord", Area: "nord-serverraum", Dept: "it", Manager: "christian.hoffmann", Role: roleSecurity,
		Teams: []simMembership{{teamSec, ""}}, Persona: "Security analyst Nord"},
	// External vendor.
	{Login: "vendor.mueller", Given: "Klaus", Family: "Müller (KIS-Hersteller)", Site: "zentrale", Dept: "vendor", Role: roleVendor,
		Teams: []simMembership{{teamVendor, ""}}, Persona: "Vendor support engineer"},
	{Login: "vendor.schmidt", Given: "Petra", Family: "Schmidt (KIS-Hersteller)", Site: "zentrale", Dept: "vendor", Role: roleVendor,
		Teams: []simMembership{{teamVendor, ""}}, Persona: "Vendor consultant"},
	// Clinical and administrative staff (employee baseline, no role).
	{Login: "katharina.brandt", Prefix: "Dr.", Given: "Katharina", Family: "Brandt", Site: "nord", Area: "nord-notaufnahme", Dept: "notaufnahme", Persona: "Doctor (Oberärztin Notaufnahme)"},
	{Login: "marcel.voigt", Given: "Marcel", Family: "Voigt", Site: "nord", Area: "nord-notaufnahme", Dept: "notaufnahme", Manager: "katharina.brandt", Persona: "Nurse Notaufnahme"},
	{Login: "sven.lindner", Prefix: "Dr.", Given: "Sven", Family: "Lindner", Site: "nord", Area: "nord-radiologie", Dept: "radiologie", Persona: "Radiologist"},
	{Login: "petra.ostermann", Given: "Petra", Family: "Ostermann", Site: "nord", Area: "nord-radiologie", Dept: "radiologie", Manager: "sven.lindner", Persona: "MTRA Radiologie"},
	{Login: "elke.fischer", Given: "Elke", Family: "Fischer", Site: "nord", Area: "nord-pflege", Dept: "pflege", Persona: "Pflegedienstleitung"},
	{Login: "sabine.hartmann", Given: "Sabine", Family: "Hartmann", Site: "nord", Area: "nord-station3b", Dept: "station3b", Manager: "elke.fischer", Persona: "Nurse (Stationsleitung 3B)"},
	{Login: "jonas.wagner", Given: "Jonas", Family: "Wagner", Site: "nord", Area: "nord-station3b", Dept: "station3b", Manager: "sabine.hartmann", Persona: "Nurse Station 3B"},
	{Login: "anja.reuter", Prefix: "Dr.", Given: "Anja", Family: "Reuter", Site: "nord", Area: "nord-labor", Dept: "labor", Persona: "Laborleitung"},
	{Login: "kemal.yilmaz", Given: "Kemal", Family: "Yilmaz", Site: "nord", Area: "nord-labor", Dept: "labor", Manager: "anja.reuter", Persona: "MTLA Labor"},
	{Login: "thomas.krause", Prefix: "Dr.", Given: "Thomas", Family: "Krause", Site: "sued", Area: "sued-chirurgie", Dept: "chirurgie", Persona: "Doctor Klinik Süd"},
	{Login: "birgit.lang", Given: "Birgit", Family: "Lang", Site: "sued", Area: "sued-pflege", Dept: "pflege", Manager: "thomas.krause", Persona: "Nurse Klinik Süd"},
	{Login: "monika.schaefer", Given: "Monika", Family: "Schäfer", Site: "zentrale", Area: "zentrale-hr", Dept: "hr", Persona: "Admin staff HR"},
	{Login: "rainer.becker", Given: "Rainer", Family: "Becker", Site: "zentrale", Area: "zentrale-einkauf", Dept: "einkauf", Manager: "monika.schaefer", Persona: "Admin staff Einkauf"},
	{Login: "claudia.neumann", Given: "Claudia", Family: "Neumann", Site: "zentrale", Area: "zentrale-buchhaltung", Dept: "buchhaltung", Manager: "monika.schaefer", Persona: "Admin staff Buchhaltung"},
}

// ---- infrastructure topology (the only hierarchy: Site -> Building -> Room -> Rack) ----

type simBuilding struct {
	Site, Name string
	Rooms      []simRoom
}

type simRoom struct{ Name, Floor string }

var simBuildings = []simBuilding{
	{"nord", "Haus A", []simRoom{{"Notaufnahme", "EG"}, {"Radiologie", "UG"}, {"Serverraum", "UG"}}},
	{"nord", "Haus B", []simRoom{{"Station 3B", "3. OG"}, {"Labor", "1. OG"}}},
	{"sued", "Hauptgebäude", []simRoom{{"Chirurgie", "1. OG"}, {"Technikraum", "UG"}}},
	{"zentrale", "Verwaltungsgebäude", []simRoom{{"Rechenzentrum", "UG"}, {"Büros", "2. OG"}}},
}

// ---- products and assets ----

type simProduct struct{ Name, MPN, IPN, Manufacturer, Category string }

var simProducts = []simProduct{
	{"Workstation WS-100", "NT-WS100", "WS-100", "Nordtech Systems", "Workstations"},
	{"Notebook Clinic 14", "NT-CL14", "NB-CL14", "Nordtech Systems", "Notebooks"},
	{"Thin Client TC-20", "NT-TC20", "TC-20", "Nordtech Systems", "Thin Clients"},
	{"Laserdrucker LP-400", "NT-LP400", "LP-400", "Nordtech Systems", "Drucker"},
	{"Etiketten- und Armbanddrucker WB-10", "NT-WB10", "WB-10", "Nordtech Systems", "Drucker"},
	{"Bettplatz-Terminal MT-15", "MD-MT15", "MT-15", "Meditron IT", "Medizin-IT"},
	{"Mobiler Visitenwagen VC-2", "MD-VC2", "VC-2", "Meditron IT", "Medizin-IT"},
	{"Befundmonitor DM-21", "MD-DM21", "DM-21", "Meditron IT", "Medizin-IT"},
	{"Access Point AP-310", "AL-AP310", "AP-310", "AirLink Networks", "Netzwerk"},
	{"Access-Switch SW-48", "AL-SW48", "SW-48", "AirLink Networks", "Netzwerk"},
}

// simLegacyNames are the English names of earlier seed runs; the seed renames those products and categories in place
// (the product by its new name, the category by its new name).
var simLegacyNames = map[string]string{
	"Thin Client TC-20": "Thin client TC-20", "Laserdrucker LP-400": "Laser printer LP-400",
	"Etiketten- und Armbanddrucker WB-10": "Label and wristband printer WB-10", "Bettplatz-Terminal MT-15": "Bedside terminal MT-15",
	"Mobiler Visitenwagen VC-2": "Mobile visit cart VC-2", "Befundmonitor DM-21": "Diagnostic monitor DM-21",
	"Access Point AP-310": "Access point AP-310", "Access-Switch SW-48": "Access switch SW-48",
	"Thin Clients": "Thin clients", "Drucker": "Printers", "Medizin-IT": "Medical IT", "Netzwerk": "Network",
}

// simAsset is one asset to register. Holder is "user:<login>", "team:<key>", "location:<area>" or "" (spare).
// Repair sends the asset to repair with that reason instead of assigning it.
type simAsset struct {
	Part, Serial, Tag, Area, Holder, Repair string
}

// hospitalAssets builds the 60 simulated assets with stable serial numbers and tags.
func hospitalAssets() []simAsset {
	var out []simAsset
	counters := map[string]int{}
	tag := 0
	add := func(part, area, holder, repair string) {
		counters[part]++
		tag++
		out = append(out, simAsset{
			Part: part, Serial: fmt.Sprintf("SIM-%s-%03d", part, counters[part]), Tag: fmt.Sprintf("SIM-%04d", tag),
			Area: area, Holder: holder, Repair: repair,
		})
	}
	// 14 workstations: one per desk user, one shared, one spare.
	for _, w := range [][2]string{
		{"nord-notaufnahme", "katharina.brandt"}, {"nord-notaufnahme", "marcel.voigt"}, {"nord-radiologie", "sven.lindner"},
		{"nord-radiologie", "petra.ostermann"}, {"nord-station3b", "sabine.hartmann"}, {"nord-station3b", "jonas.wagner"},
		{"nord-pflege", "elke.fischer"}, {"nord-labor", "anja.reuter"}, {"nord-labor", "kemal.yilmaz"},
		{"zentrale-hr", "monika.schaefer"}, {"zentrale-einkauf", "rainer.becker"}, {"zentrale-buchhaltung", "claudia.neumann"},
	} {
		add("WS-100", w[0], "user:"+w[1], "")
	}
	add("WS-100", "nord-notaufnahme", "location:nord-notaufnahme", "")
	add("WS-100", "zentrale-rz", "", "")
	// 4 notebooks.
	add("NB-CL14", "sued-chirurgie", "user:thomas.krause", "")
	add("NB-CL14", "sued-pflege", "user:birgit.lang", "")
	add("NB-CL14", "nord-serverraum", "user:christian.hoffmann", "")
	add("NB-CL14", "nord-serverraum", "user:oliver.stein", "")
	// 8 thin clients in wards.
	for _, a := range []string{"nord-station3b", "nord-station3b", "nord-notaufnahme", "nord-notaufnahme", "nord-labor", "sued-pflege", "sued-pflege", "sued-chirurgie"} {
		add("TC-20", a, "location:"+a, "")
	}
	// 6 bedside terminals.
	for _, a := range []string{"nord-station3b", "nord-station3b", "nord-station3b", "nord-notaufnahme", "sued-pflege", "sued-pflege"} {
		add("MT-15", a, "location:"+a, "")
	}
	// 4 visit carts, one in repair.
	add("VC-2", "nord-station3b", "location:nord-station3b", "")
	add("VC-2", "nord-station3b", "", "Akku defekt, Austausch beim Hersteller")
	add("VC-2", "nord-notaufnahme", "location:nord-notaufnahme", "")
	add("VC-2", "sued-pflege", "location:sued-pflege", "")
	// 3 diagnostic monitors.
	for i := 0; i < 3; i++ {
		add("DM-21", "nord-radiologie", "location:nord-radiologie", "")
	}
	// 6 laser printers.
	for _, a := range []string{"nord-notaufnahme", "nord-station3b", "nord-radiologie", "zentrale-einkauf", "zentrale-hr", "sued-pflege"} {
		add("LP-400", a, "location:"+a, "")
	}
	// 3 label and wristband printers, one in repair.
	add("WB-10", "nord-notaufnahme", "", "Thermokopf defekt, Ersatzteil bestellt")
	add("WB-10", "nord-labor", "location:nord-labor", "")
	add("WB-10", "sued-pflege", "location:sued-pflege", "")
	// 10 access points, owned by the WLAN team.
	for _, a := range []string{"nord-notaufnahme", "nord-radiologie", "nord-station3b", "nord-station3b", "nord-labor", "nord-pflege", "sued-chirurgie", "sued-pflege", "sued-technik", "zentrale-einkauf"} {
		add("AP-310", a, "team:"+teamWLAN, "")
	}
	// 2 switches, owned by Infrastruktur.
	add("SW-48", "nord-serverraum", "team:"+teamInfra, "")
	add("SW-48", "sued-technik", "team:"+teamInfra, "")
	return out
}

// ---- tickets ----

// simTicket is one ticket. Steps are lifecycle operations after assignment: start, wait:<reason>,
// resolve:<text>, close, cancel:<reason>, reopen:<reason>. Device attaches the reporter's own workstation.
type simTicket struct {
	Title, Description, Reporter, Priority, Queue, Assignee string
	Device                                                  bool
	Steps                                                   []string
	Comments                                                []simComment
}

type simComment struct {
	By       string
	Internal bool
	Body     string
}

var simTickets = []simTicket{
	{Title: "ORBIS: Anmeldung am Arbeitsplatz Notaufnahme dauert über 2 Minuten", Description: "Seit heute früh dauert die Anmeldung an ORBIS extrem lange. Mehrere Kollegen betroffen, Patientenaufnahme stockt.",
		Reporter: "katharina.brandt", Priority: "urgent", Queue: teamKIS, Assignee: "henrik.vogel", Device: true, Steps: []string{"start"},
		Comments: []simComment{{"henrik.vogel", false, "Wir prüfen den Anwendungsserver. Bitte nicht mehrfach neu anmelden."}, {"henrik.vogel", true, "Last auf App-Server 2 auffällig, Verdacht auf hängenden Dienst. Hersteller informiert."}}},
	{Title: "ORBIS: Medikationsmodul zeigt Fehlermeldung Zeitüberschreitung", Description: "Beim Öffnen der Medikationsübersicht erscheint nach ca. 30 Sekunden 'Zeitüberschreitung'. Tritt mehrmals pro Schicht auf.",
		Reporter: "sabine.hartmann", Priority: "high", Queue: teamKIS},
	{Title: "ORBIS: Neuer Benutzer kann Leistungsanforderung nicht öffnen", Description: "Neue Pflegekraft hat Zugang, aber der Menüpunkt Leistungsanforderung fehlt.",
		Reporter: "jonas.wagner", Queue: teamKIS, Assignee: "sandra.winter"},
	{Title: "ORBIS: Befund wird im Radiologie-Arbeitsplatz nicht angezeigt", Description: "Befunde der letzten 24 Stunden werden für einzelne Patienten nicht angezeigt.",
		Reporter: "sven.lindner", Priority: "high", Queue: teamKIS, Assignee: "tobias.kraft", Steps: []string{"start", "wait:vendor"},
		Comments: []simComment{{"tobias.kraft", false, "Wir warten auf Rückmeldung des KIS-Herstellers (Ticket beim Hersteller eröffnet)."}}},
	{Title: "ORBIS: Schnittstelle Labor liefert keine Ergebnisse", Description: "Laborergebnisse erscheinen verzögert oder gar nicht in der Patientenakte.",
		Reporter: "anja.reuter", Priority: "urgent", Queue: teamKIS, Assignee: "henrik.vogel", Steps: []string{"start"}},
	{Title: "WLAN: Visitenwagen verliert Verbindung auf Station 3B", Description: "Der Visitenwagen verliert auf dem Flur zwischen Zimmer 8 und 12 die Verbindung und muss neu gestartet werden.",
		Reporter: "jonas.wagner", Priority: "high", Queue: teamWLAN, Assignee: "oliver.stein", Steps: []string{"start"},
		Comments: []simComment{{"oliver.stein", true, "Roaming-Problem zwischen zwei Access Points vermutet, Messung geplant."}}},
	{Title: "WLAN: Kein Empfang im Treppenhaus Haus B", Description: "Im Treppenhaus Haus B ist kein WLAN verfügbar, DECT-Telefone brechen ab.",
		Reporter: "petra.ostermann", Queue: teamWLAN, Assignee: "nadine.roth"},
	{Title: "WLAN: Gäste-WLAN in Klinik Süd Cafeteria nicht erreichbar", Description: "Besucher können sich nicht mit dem Gäste-WLAN verbinden.",
		Reporter: "birgit.lang", Priority: "low", Queue: teamWLAN},
	{Title: "Telefon: DECT-Handy Station 3B ohne Ton", Description: "Das Stations-DECT klingelt nicht mehr.",
		Reporter: "jonas.wagner", Queue: teamWLAN, Assignee: "nadine.roth", Steps: []string{"start", "resolve:Lautsprecher-Profil zurückgesetzt, Telefon klingelt wieder."}},
	{Title: "Telefon: Rufumleitung Notaufnahme nach Dienstwechsel falsch", Description: "Nach dem Schichtwechsel werden Anrufe an das falsche Telefon umgeleitet.",
		Reporter: "marcel.voigt", Priority: "high", Queue: teamWLAN, Assignee: "nadine.roth", Steps: []string{"start", "resolve:Rufumleitung auf Dienstplan angepasst.", "close"}},
	{Title: "Drucker: Etikettendrucker Labor druckt Probenetiketten verschoben", Description: "Die Etiketten sind um ca. 3 mm nach rechts verschoben und schneiden Barcodes ab.",
		Reporter: "kemal.yilmaz", Priority: "high", Queue: teamFLS, Assignee: "lena.bauer", Steps: []string{"start"},
		Comments: []simComment{{"lena.bauer", false, "Haben Sie den Drucker schon neu kalibriert? Anleitung steht im Wissensartikel."}, {"kemal.yilmaz", false, "Ja, ohne Erfolg."}}},
	{Title: "Drucker: Netzwerkdrucker Einkauf meldet Tonerwechsel", Description: "Drucker meldet Toner leer, Ersatz ist im Schrank.",
		Reporter: "rainer.becker", Priority: "low", Queue: teamFLS, Assignee: "murat.demir", Steps: []string{"start", "resolve:Toner gewechselt."}},
	{Title: "Drucker: Patientenarmband-Drucker Aufnahme druckt nicht", Description: "Der Armband-Drucker in der Notaufnahme bleibt stumm, LED blinkt rot.",
		Reporter: "katharina.brandt", Priority: "urgent", Queue: teamFLS, Assignee: "lena.bauer", Steps: []string{"start", "wait:hardware"},
		Comments: []simComment{{"lena.bauer", false, "Thermokopf ist defekt, Ersatzteil bestellt. Bis dahin Ausweichdrucker Station 3B nutzen."}}},
	{Title: "Passwort zurücksetzen: Mitarbeiterin HR gesperrt", Description: "Konto ist nach mehreren Fehlversuchen gesperrt.",
		Reporter: "monika.schaefer", Queue: teamFLS, Assignee: "murat.demir", Steps: []string{"start", "resolve:Konto entsperrt, neues Passwort telefonisch übergeben.", "close"}},
	{Title: "Neuer Monitor für Buchhaltung", Description: "Der zweite Monitor ist defekt, bitte ersetzen (Bildstörungen).",
		Reporter: "claudia.neumann", Priority: "low"},
	{Title: "Outlook: Gemeinsames Postfach Einkauf nicht sichtbar", Description: "Das gemeinsame Postfach Einkauf erscheint nicht mehr in Outlook.",
		Reporter: "rainer.becker", Queue: teamFLS, Assignee: "murat.demir"},
	{Title: "Verdacht auf Phishing-Mail an Verwaltung", Description: "Mehrere Kollegen haben eine Rechnung von einem unbekannten Absender erhalten, ein Anhang wurde geöffnet.",
		Reporter: "claudia.neumann", Priority: "high", Queue: teamSec, Assignee: "ines.falk",
		Comments: []simComment{{"ines.falk", true, "Anhang im Sandbox-Test: Makro lädt Nachladecode. Betroffene Geräte isolieren."}}},
	{Title: "USB-Stick mit Patientendaten auf Station gefunden", Description: "Ein USB-Stick ohne Beschriftung lag im Stationszimmer, vermutlich mit Patientendaten.",
		Reporter: "sabine.hartmann", Priority: "urgent", Queue: teamSec, Assignee: "deniz.arslan", Steps: []string{"start"}},
	{Title: "Serverraum Nord: Temperaturwarnung Rack 3", Description: "Die Raumüberwachung meldet seit 06:10 Uhr erhöhte Temperatur an Rack 3.",
		Reporter: "christian.hoffmann", Priority: "high", Queue: teamInfra, Assignee: "uwe.pohl"},
	{Title: "Backup-Job KIS-Datenbank gestern fehlgeschlagen", Description: "Der nächtliche Backup-Job für die ORBIS-Datenbank ist mit Fehler 112 abgebrochen.",
		Reporter: "henrik.vogel", Priority: "high", Queue: teamInfra, Assignee: "mirja.engel", Steps: []string{"start"}},
	{Title: "Switch Klinik Süd Etage 2: Ports ausgefallen", Description: "Auf Etage 2 sind mehrere Netzwerkdosen ohne Link.",
		Reporter: "birgit.lang", Priority: "high", Queue: teamInfra, Assignee: "mirja.engel", Steps: []string{"start", "wait:hardware"}},
	{Title: "Befundmonitor Radiologie: Kalibrierung abgelaufen", Description: "Die Qualitätssicherung meldet eine abgelaufene Kalibrierung an Befundmonitor 2.",
		Reporter: "sven.lindner", Queue: teamFLS},
	{Title: "Notebook für Chefarztvisite defekt (Display)", Description: "Das Display hat Streifen. Ersatzgerät wird nicht mehr benötigt (Doppelmeldung).",
		Reporter: "thomas.krause", Queue: teamFLS, Assignee: "murat.demir", Steps: []string{"cancel:Doppelte Meldung, bereits unter anderer Nummer erfasst."}},
	{Title: "Zugriff auf Laufwerk Labor-Auswertungen fehlt", Description: "Nach dem Gruppenwechsel ist das Laufwerk nicht mehr verbunden.",
		Reporter: "kemal.yilmaz", Queue: teamFLS, Assignee: "lena.bauer", Steps: []string{"start", "resolve:Gruppenmitgliedschaft ergänzt.", "reopen:Laufwerk fehlt nach Neustart wieder."}},
	{Title: "Dienstplan-Software startet nicht (Notaufnahme)", Description: "Die Dienstplan-Anwendung öffnet sich nicht mehr.",
		Reporter: "marcel.voigt", Queue: teamFLS, Assignee: "lena.bauer", Device: true, Steps: []string{"start", "resolve:Anwendung neu installiert."}},
	{Title: "Bildschirm flackert am Stationsarbeitsplatz 3B", Description: "Der Bildschirm flackert gelegentlich.",
		Reporter: "jonas.wagner", Priority: "low", Queue: teamFLS, Assignee: "lena.bauer", Device: true, Steps: []string{"start", "wait:customer"}},
	{Title: "Anfrage: zweiter Monitor für Pflegedienstleitung", Description: "Für den Dienstplan bitte einen zweiten Monitor.",
		Reporter: "elke.fischer", Priority: "low"},
}

// simProblems are the known issues. Tickets are linked by title; Ops are problem operations with their text.
type simProblem struct {
	Title, Description, Owner string
	Ops                       []simProblemOp
	Tickets                   []string
}

type simProblemOp struct{ Op, Text string }

var simProblems = []simProblem{
	{Title: "ORBIS: Sporadische Zeitüberschreitung im Medikationsmodul", Description: "Das Medikationsmodul antwortet unregelmäßig nicht. Betrifft mehrere Stationen.", Owner: "henrik.vogel",
		Ops:     []simProblemOp{{"identify_cause", "Der Anwendungsdienst für Medikation hängt nach Verbindungsabbruch zur Datenbank."}, {"mark_known_error", "Dienst 'ORBISMed' auf dem App-Server neu starten; ORBIS-Technik kann das innerhalb von 5 Minuten."}},
		Tickets: []string{"ORBIS: Medikationsmodul zeigt Fehlermeldung Zeitüberschreitung", "ORBIS: Anmeldung am Arbeitsplatz Notaufnahme dauert über 2 Minuten"}},
	{Title: "WLAN: Roaming-Abbrüche der Visitenwagen zwischen AP-Zonen Station 3B", Description: "Visitenwagen verlieren beim Wechsel zwischen zwei Access Points die Verbindung.", Owner: "oliver.stein",
		Ops:     []simProblemOp{{"identify_cause", "Roaming-Schwellwert der Access Points zu niedrig eingestellt."}, {"mark_known_error", "Wagen am Flurende kurz anhalten, WLAN aus- und einschalten; Techniker kann das Profil des Wagens neu laden."}, {"plan_resolution", ""}},
		Tickets: []string{"WLAN: Visitenwagen verliert Verbindung auf Station 3B"}},
	{Title: "Etikettendrucker Labor: Versatz nach Windows-Update", Description: "Nach dem Monatsupdate sind Etiketten verschoben.", Owner: "lena.bauer",
		Ops: []simProblemOp{{"investigate", ""}}, Tickets: []string{"Drucker: Etikettendrucker Labor druckt Probenetiketten verschoben"}},
}

// ---- major incident ----

type simMajor struct {
	Title, Summary string
	Tickets        []string
}

var simMajorIncident = simMajor{
	Title: "ORBIS: Anmeldung am Standort Nord gestört", Summary: "Die Anmeldung an ORBIS dauert am Standort Nord sehr lange. Die Ursache wird untersucht, der Betrieb mit Papierunterlagen ist vorbereitet.",
	Tickets: []string{"ORBIS: Anmeldung am Arbeitsplatz Notaufnahme dauert über 2 Minuten", "ORBIS: Schnittstelle Labor liefert keine Ergebnisse"},
}

// ---- tasks ----

type simTask struct {
	Title, Description, Priority, Team, User string
	DueInDays                                int
}

var simTasks = []simTask{
	{Title: "KIS-Hersteller: Hotfix 2026.3.1 für Medikationsmodul im Testsystem einspielen", Description: "Hotfix laut Herstellerhinweis im Testsystem installieren und Rückmeldung geben.", Priority: "high", Team: teamVendor, DueInDays: 3},
	{Title: "Telefonie & WLAN: Firmware-Update der Access Points Klinik Süd vorbereiten", Description: "Wartungsfenster abstimmen und Rollback-Plan schreiben.", Priority: "normal", Team: teamWLAN, DueInDays: 10},
	{Title: "Security: Quartalsweise Berechtigungsprüfung ORBIS", Description: "Berechtigungen der Rolle 'Arzt' mit dem Fachbereich abgleichen.", Priority: "normal", Team: teamSec, DueInDays: 14},
	{Title: "First Level: Ersatz-Etikettendrucker für das Labor bereitstellen", Description: "Ersatzdrucker vorkonfigurieren und zur Laborleitung bringen.", Priority: "high", Team: teamFLS, User: "lena.bauer", DueInDays: 1},
	{Title: "Infrastruktur: USV-Test Serverraum Nord durchführen", Description: "Halbjährlicher Test, Ergebnis dokumentieren.", Priority: "normal", Team: teamInfra, DueInDays: 7},
}

// ---- briefing ----

type simBriefing struct {
	Title, Body, Severity string
	ValidDays             int
}

var simBriefings = []simBriefing{
	{"Wartungsfenster ORBIS am Samstag 22:00 bis 02:00 Uhr", "Das KIS ist in dieser Zeit eingeschränkt. Papier-Notfallprozess ist vorbereitet; ORBIS/KIS-Team besetzt.", "warning", 7},
	{"Phishing-Welle: gefälschte Rechnungen an die Verwaltung", "Bitte keine Anhänge unbekannter Absender öffnen. Verdachtsfälle als Ticket an Security melden.", "critical", 5},
	{"Neues Medizin-WLAN-Profil auf Station 3B ab Montag", "Visitenwagen erhalten ein neues WLAN-Profil. Bei Problemen Ticket mit Gerätenummer erstellen.", "info", 14},
}

// ---- knowledge ----

type simArticle struct{ Title, Summary, Body, Audience string }

var simArticles = []simArticle{
	{"ORBIS: Anmeldung schlägt fehl oder dauert lange", "Erste Schritte, wenn ORBIS nicht startet.", "1. Nicht mehrfach neu anmelden.\n2. Prüfen, ob andere Arbeitsplätze betroffen sind.\n3. Ticket mit Arbeitsplatz-Nummer und Uhrzeit erstellen.\n4. Bei Patientengefährdung den Notfallprozess nutzen und die IT telefonisch informieren.", "employee"},
	{"ORBIS: Anmeldung mit Chipkarte einrichten", "Einmalige Einrichtung der Kartenanmeldung.", "Karte in den Leser stecken, ORBIS starten, 'Karte registrieren' wählen und die PIN festlegen. Bei Fehlern meldet sich der First Level Support.", "employee"},
	{"ORBIS: Medikationsmodul Zeitüberschreitung (Known Error)", "Interner Workaround für die ORBIS-Technik.", "Dienst ORBISMed auf dem App-Server neu starten, danach Medikationsmodul testen. Dauer ca. 5 Minuten. Ticket an das Problem 'Sporadische Zeitüberschreitung' hängen.", "internal"},
	{"ORBIS: Berechtigungen für neue Mitarbeitende beantragen", "Wie ORBIS-Zugriff beantragt wird.", "Über den Katalog 'ORBIS-Zugang beantragen' mit Fachbereich und Rolle. Die Genehmigung erfolgt durch die Vorgesetzten.", "employee"},
	{"WLAN: Verbindung mit Visitenwagen und mobilen Geräten", "Wenn der Visitenwagen die Verbindung verliert.", "Wagen anhalten, WLAN aus- und einschalten, 30 Sekunden warten. Hilft das nicht: Ticket mit Wagennummer und Standort erstellen.", "employee"},
	{"WLAN: Gerät im Medizin-WLAN registrieren", "Interne Checkliste Telefonie & WLAN.", "MAC-Adresse erfassen, Gerät im Inventar prüfen, Profil 'medizin' vergeben, Eintrag im Ticket dokumentieren. Security muss neue Gerätetypen vorher freigeben.", "internal"},
	{"WLAN: Access Point austauschen (Checkliste)", "Austausch ohne Versorgungslücke.", "Wartungsfenster abstimmen, Port am Switch prüfen, neuen AP mit gleichem Profil adoptieren, alten AP ausbuchen, Asset aktualisieren.", "internal"},
	{"Drucker: Etikettendrucker kalibrieren", "Etiketten sitzen verschoben.", "Drucker ausschalten, Feed-Taste halten und einschalten, bis die LED zweimal blinkt. Danach eine Testseite drucken.", "employee"},
	{"Drucker: Netzwerkdrucker hinzufügen", "So wird ein Drucker am Arbeitsplatz eingerichtet.", "Start > Drucker > Hinzufügen, Druckername der Station wählen (steht am Gerät). Bei Fehlern ein Ticket mit dem Namen des Druckers erstellen.", "employee"},
	{"Drucker: Patientenarmband-Drucker Störungsbehebung", "Interne Störungsliste.", "LED rot: Thermokopf oder Papier prüfen. Ersatz: Gerät aus Station 3B nutzen und Ticket an First Level mit Priorität hoch.", "internal"},
	{"Phishing erkennen und melden", "Verdacht auf Phishing-Mail.", "Anhang nicht öffnen, Mail nicht weiterleiten, Ticket an Security erstellen. Wurde ein Anhang geöffnet: Gerät vom Netz trennen und IT anrufen.", "employee"},
	{"First Level: Priorität festlegen (Triage)", "Interner Leitfaden zur Priorisierung.", "Urgent: Patientenversorgung gefährdet oder Datenverlust. High: Abteilung eingeschränkt. Normal: einzelner Arbeitsplatz. Low: Komfort. Warteschlange nach Fachgebiet wählen (ORBIS/KIS, Telefonie & WLAN, Infrastruktur, Security).", "internal"},
	{"Dienstleister KIS-Hersteller: Regeln für den Zugriff", "Interne Regeln für externe Dienstleister.", "Dienstleister arbeiten nur an ihnen zugewiesenen Aufgaben. Remote-Zugriff nur nach Freigabe durch ORBIS/KIS und mit Ticket. Keine Weitergabe von Zugangsdaten.", "internal"},
}

// ---- catalog ----

// simCatalogKeys are the catalog items created by seed-hospital (definitions are built at run time because
// they reference Team ids).
var simCatalogKeys = []string{"orbis-access", "medical-device-network", "dect-phone", "printer-setup"}
