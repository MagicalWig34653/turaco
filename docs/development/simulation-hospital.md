# Hospital IT Simulation (usability testing)

A reproducible dataset and persona guide for usability tests: the IT department of a hospital group with three sites, five technical teams, an external software vendor and clinical and administrative staff. It is **development data only**: every login uses the same public password, and `turaco-admin demo seed-hospital` refuses to run unless `APP_ENV=development`.

Technical identifiers are English; the simulated organization, tickets and articles are German test data. The base demo seed (`demo seed`: catalog items and their form labels, product categories, products, help articles) is German too; a re-run renames the English records of earlier runs in place (catalog items by key, products and categories by their old name) and retires the English help articles. The manager-approval catalog items get the Team Standort-IT-Leitung as fallback approver (`fallbackTeamId`), so a request of a person without a manager no longer dead-ends. Interface strings still come from the i18n resources.

## Load the scenario

```bash
make dev-setup                                   # once: infrastructure, migrations, devadmin, base demo seed
./scripts/with-env.sh go run ./backend/cmd/turaco-admin demo seed-hospital
```

- The command is idempotent. A second run creates nothing and does not reset anything that testers changed; it only adds missing parts and restores the permissions of the simulation roles.
- It does not call `demo seed`. Run `make dev-setup` (or `demo seed`) first if you also want the generic demo catalog items.
- For load and concurrency tests with the same logins see [Load testing](load-testing.md).
- To return to the baseline after testing, recreate the development database (stop the infrastructure, remove the PostgreSQL volume defined in `deploy/compose/dev.yaml`, run `make dev-setup`, then `demo seed-hospital`).
- Local logins are emergency-style accounts. The API must run with `AUTH_EMERGENCY_LOGIN_ENABLED=true` (the **Turaco API** run configuration does). Sessions of these accounts end after one hour at the latest, and every login is logged at error level; both are expected.
- Everything the seed writes is audited with the CLI actor and the correlation id `demo-seed-hospital`. Locations, departments, Teams, roles and role assignments are created through the audited People and role operations; only the profile attributes of the emergency-account personas are written directly (see [How the seed writes organization data](#how-the-seed-writes-organization-data)).

**Password of every simulation login: `turaco-sim-password`** (development only; public). `devadmin` / `turaco-dev-password` remains the platform administrator for setup and for observing as an admin.

## Scenario

A university hospital group.

| Site | What it is | Locations (areas) |
|---|---|---|
| Universitätsklinikum Nord (Hauptcampus) | main campus, most clinical staff and the data center for ORBIS/KIS | Notaufnahme, Radiologie, Station 3B, Pflegedienstleitung, Labor, Serverraum Haus A |
| Klinik Süd | surgical clinic | Chirurgie, Pflege, Technikraum |
| Verwaltung / Zentrale | administration and second data center | Personalabteilung, Einkauf, Buchhaltung, Rechenzentrum |

**Hierarchy.** Organization Locations are flat (no parent). The seed creates the three sites and 13 areas as Locations (external keys `SIM-SITE-*`, `SIM-AREA-*`; names such as "Nord – Station 3B") and, as the only real hierarchy, Infrastructure Buildings and Rooms below the sites (for example Nord > Haus A > Serverraum with "Rack 1"). Assets point at the area Location.

### IT organization

| Team (queue) | Members | Role key | Focus |
|---|---|---|---|
| First Level Support | 2 | `first-level-support` | triage and everyday tickets |
| Telefonie & WLAN | 3 | `it-specialist` | WLAN, DECT, telephony |
| ORBIS/KIS | 3 | `it-specialist` | hospital information system |
| Infrastruktur | 2 | `infrastructure-engineer` | servers, network, backup, racks, changes; **not primarily ticket work** |
| Security | 2 | `security-analyst` | advisories, findings, incidents; **not primarily ticket work** |
| Standort-IT-Leitung | 3 | `it-site-lead` | one Standort-IT-Leiter per site |
| Dienstleister KIS-Hersteller | 2 | `vendor-restricted` | external vendor, **no staff access** |

Interpretation of the brief: the three IT site leads are additional people who form their own team; the five technical teams have exactly the sizes listed. Each technical team has a member with the membership role "Teamleitung". Every IT person has a manager (the lead of the site) and a primary location.

The vendor is modelled as a **Team**, because Organization Directory Groups are only created by directory synchronization. Their users are ordinary active users with the role `vendor-restricted`.

### Recurring meetings (real-life, not tool features)

- **Daily 08:00 stand-up** of the three site leads (`christian.hoffmann` hosts; `silke.brandl`, `martin.kessler`). No vendor. Material: the Overview page (briefing feed), the Service Desk queue, open Major Incidents, Problems and Known Errors.
- **Weekly all-IT meeting** with all five technical teams and the leads. No vendor. Material: Problems, Changes and the maintenance calendar, Security overview, tasks per team.
- **Vendor** takes part in neither. The vendor works only through what the restricted role allows (see [Vendor persona](#vendor-kis-hersteller)).

Meetings or a calendar do not exist in Turaco (see the gap list); testers run the meetings on paper or in a call and use Turaco as the information source.

## Seeded data

| Area | Count | Notes |
|---|---|---|
| Users | 31 | 15 IT (incl. 3 leads), 2 vendor, 14 clinical and administrative staff; each with name, email (`*.example`), department, primary location, manager |
| Roles | 6 custom | see [Roles](#roles); employees need no role |
| Assets | 60 | workstations, notebooks, thin clients, bedside terminals, visit carts, diagnostic monitors, laser and wristband printers, WLAN access points (team-held), switches (team-held); person-, location- or team-assigned; one spare workstation, two devices in repair |
| Queues | 5 | the platform's default desk `it` (prefix `TKT`, renamed "Allgemein (First Level)", intake desk of First Level Support, site leads and vendor tickets) plus `telefonie-wlan` (`TEL`), `orbis-kis` (`KIS`), `infrastruktur` (`INF`) and `security` (`SEC`); each specialist desk is internal, routes to its Team, grants the owning Team and the site-lead Team the work level and First Level Support the create level (people with the global `tickets.manage` or `tickets.view` see every desk anyway). A re-run moves tickets of earlier runs that still sit in the intake desk into the desk of their routing Team (new number, old number kept as an alias); finished tickets stay where they are |
| Tickets | 27 | all states, four priorities, routing Teams of all five teams, each in the desk of its Team; comments (public and internal), some with the reporter's device attached |
| Problems | 3 | two Known Errors with workaround (ORBIS medication timeout, WLAN roaming), one under investigation |
| Major incident | 1 | "ORBIS: Anmeldung am Standort Nord gestört" with two linked tickets |
| Knowledge | 14 articles | ORBIS, WLAN, printing, phishing, triage guide, vendor rules (employee and internal audiences) |
| Tasks | 5 | one for the vendor team and one each for First Level Support, Telefonie & WLAN, Infrastruktur and Security (all assigned to the team) |
| Briefing | 3 published items | ORBIS maintenance window, phishing wave, new WLAN profile |
| Catalog items | 4 | ORBIS access, medical device network, DECT phone, printer setup (team-routed fulfillment tasks; one with Security as approver team) |
| Infrastructure | 4 buildings, 9 rooms, 2 racks | Nord, Süd, Zentrale |

Asset serial numbers are `SIM-<part>-<nnn>` (for example `SIM-WS-100-001`), tags `SIM-0001` to `SIM-0060`. A workstation belongs to each desk user (`katharina.brandt` has `SIM-WS-100-001`).

## Roles

The seed creates these roles through the audited role service (visible under Administration > Roles) and assigns them to users directly. Permissions are global (there is no site scope). The built-in "employee" baseline needs no role: every signed-in user can raise tickets and read their own, read published employee articles and see their own assets.

| Role key | Intended for | Key permissions |
|---|---|---|
| `first-level-support` | First level | `tickets.manage`, read knowledge/assets/devices, `requests.view`, tasks of own team, attended remote-access start |
| `it-specialist` | WLAN and ORBIS technicians | first level plus `assets.manage`, `knowledge.manage`, `problems.manage`, `tasks.manage`, read changes/infrastructure/security |
| `it-site-lead` | Standort-IT-Leiter | specialist plus `briefing.manage`, `majorincidents.manage`, `changes.manage`/`approve`, `requests.manage`, `organization.teams.manage`, presence availability/entries of the team |
| `security-analyst` | Security | `security.manage`, `security.accept_risk`, read tickets (`tickets.view`, cannot work them), audit log, directory groups |
| `infrastructure-engineer` | Infrastruktur | `infrastructure.manage`, `changes.manage`/`execute`, `services.manage`, `assets.manage`; tickets read-only (`tickets.view`) |
| `vendor-restricted` | KIS vendor | only `tasks.work` (tasks assigned to the vendor team or the person) plus the employee baseline |

## Personas and logins

All passwords are `turaco-sim-password`.

### IT

| Login | Name | Site | Team / role | Persona |
|---|---|---|---|---|
| `christian.hoffmann` | Christian Hoffmann | Nord | Standort-IT-Leitung, `it-site-lead` | IT lead Nord, hosts the 08:00 stand-up |
| `silke.brandl` | Silke Brandl | Süd | Standort-IT-Leitung, `it-site-lead` | IT lead Süd |
| `martin.kessler` | Martin Kessler | Zentrale | Standort-IT-Leitung, `it-site-lead` | IT lead Zentrale |
| `lena.bauer` | Lena Bauer | Nord | First Level Support (Teamleitung), `first-level-support` | first-level triage |
| `murat.demir` | Murat Demir | Süd | First Level Support, `first-level-support` | first level Süd |
| `oliver.stein` | Oliver Stein | Nord | Telefonie & WLAN (Teamleitung), `it-specialist` | WLAN technician |
| `nadine.roth` | Nadine Roth | Nord | Telefonie & WLAN, `it-specialist` | telephony technician |
| `jens.albrecht` | Jens Albrecht | Süd | Telefonie & WLAN, `it-specialist` | WLAN technician Süd |
| `henrik.vogel` | Henrik Vogel | Nord | ORBIS/KIS (Teamleitung), `it-specialist` | ORBIS technician |
| `sandra.winter` | Sandra Winter | Nord | ORBIS/KIS, `it-specialist` | ORBIS technician |
| `tobias.kraft` | Tobias Kraft | Zentrale | ORBIS/KIS, `it-specialist` | ORBIS technician Zentrale |
| `uwe.pohl` | Uwe Pohl | Nord | Infrastruktur (Teamleitung), `infrastructure-engineer` | infrastructure specialist |
| `mirja.engel` | Mirja Engel | Süd | Infrastruktur, `infrastructure-engineer` | infrastructure specialist Süd |
| `ines.falk` | Ines Falk | Zentrale | Security (Teamleitung), `security-analyst` | security analyst |
| `deniz.arslan` | Deniz Arslan | Nord | Security, `security-analyst` | security analyst Nord |

### Vendor

| Login | Name | Team / role | Persona |
|---|---|---|---|
| `vendor.mueller` | Klaus Müller (KIS-Hersteller) | Dienstleister KIS-Hersteller, `vendor-restricted` | vendor support engineer |
| `vendor.schmidt` | Petra Schmidt (KIS-Hersteller) | Dienstleister KIS-Hersteller, `vendor-restricted` | vendor consultant |

### Hospital staff (employee baseline, no role)

| Login | Name | Where | Persona |
|---|---|---|---|
| `katharina.brandt` | Dr. Katharina Brandt | Nord, Notaufnahme | doctor (Oberärztin) |
| `marcel.voigt` | Marcel Voigt | Nord, Notaufnahme | nurse |
| `sven.lindner` | Dr. Sven Lindner | Nord, Radiologie | radiologist |
| `petra.ostermann` | Petra Ostermann | Nord, Radiologie | MTRA |
| `elke.fischer` | Elke Fischer | Nord, Pflegedienstleitung | nursing management |
| `sabine.hartmann` | Sabine Hartmann | Nord, Station 3B | nurse (Stationsleitung) |
| `jonas.wagner` | Jonas Wagner | Nord, Station 3B | nurse |
| `anja.reuter` | Dr. Anja Reuter | Nord, Labor | laboratory lead |
| `kemal.yilmaz` | Kemal Yilmaz | Nord, Labor | MTLA |
| `thomas.krause` | Dr. Thomas Krause | Süd, Chirurgie | doctor Klinik Süd |
| `birgit.lang` | Birgit Lang | Süd, Pflege | nurse Klinik Süd |
| `monika.schaefer` | Monika Schäfer | Zentrale, HR | administrative staff |
| `rainer.becker` | Rainer Becker | Zentrale, Einkauf | administrative staff |
| `claudia.neumann` | Claudia Neumann | Zentrale, Buchhaltung | administrative staff |

Managers: the nurses report to their ward lead, the ward lead to `elke.fischer`; physicians, the lab lead, `elke.fischer` and `monika.schaefer` have none. Requests that need the manager's approval therefore demonstrate both cases.

## Not implemented yet (record as findings, not failures)

Testers must write these down as **gaps**, not as defects, when they look for them. [Current status](../product/current-status.md) is authoritative.

| Expectation | State |
|---|---|
| Ticket **routing rules** (automatic assignment by category or keyword) | Not implemented; desks (queues) exist, tickets are routed by the person who raises or moves them ([F13 design](../product/f13-workbench-views-design.md)) |
| **Meetings and calendar** for recurring team meetings, agendas, minutes | Not implemented. The maintenance calendar shows change windows only |
| **Per-site scoping** of permissions (an IT lead sees only his site) | Not implemented; roles are global |
| **Vendor / external portal** with a dedicated restricted view | Not implemented. The vendor gets a minimal role instead (tasks only) |
| Ticket SLA, time tracking, escalation rules, customer satisfaction | Not implemented |
| Provider-observed **devices** (Intune sync) | Not seeded; assets exist, the device list stays empty or shows only synced data |
| Remote access sessions, deployments | Need configured providers; not part of the dataset |
| Workforce Presence, Turaco AI | Optional modules, default off (`/admin/modules`); test only after an admin enables them and meets their preconditions |
| Organization CSV import and bulk edit, External Parties with expiry and sponsor, health and setup screens, audit names and export | Not implemented (F14 A-C, A-F, A-G); the vendor persona uses the `vendor-restricted` role and an emergency-style login instead |
| Per-user passwords by invitation in the simulation | The personas are emergency accounts with one public password. The invitation flow (`AUTH_LOCAL_LOGIN_ENABLED`, [administrator guide](../operations/administrator-getting-started.md)) exists but is not part of the dataset |

## How testers work

1. Sign in as the persona (one browser profile per persona, or private windows). Sessions last at most one hour.
2. Follow the persona's script. Each script has **Do**, **Should see/be able to do** and **Must be denied** checks.
3. Record each result as **Pass**, **Defect** (behaviour contradicts the permission or the script), **Gap** (listed above or clearly missing capability) or **Usability** (works, but confusing or slow). Note login, page, what you expected, what happened, and the time.
4. Never fix data by SQL during a test; use the UI or ask the facilitator. Denied actions must be denied by the **backend**, not only hidden in the UI: also try the direct URL of a hidden page.

Useful pages: Overview `/`, My Work `/my-work`, My tickets `/support`, Service Desk `/service-desk`, Problems `/problems`, Major Incidents `/incidents`, Knowledge `/knowledge`, Catalog `/catalog`, Requests `/requests`, Assets `/assets`, Tasks `/tasks`, Briefing `/briefing`, Security `/security/overview`, Infrastructure `/infrastructure`, Changes `/changes`, Task boards `/tasks/boards`, Ticket queues administration `/service-desk/queues`, Users `/admin/users`, Teams `/admin/teams`, Roles `/admin/roles`, Modules `/admin/modules`, Audit `/admin/audit`.

## Test script catalog

### First-level triage (`lena.bauer`)

Do:
1. Open the Service Desk queue. Find the unassigned new tickets (for example "ORBIS: Medikationsmodul zeigt Fehlermeldung Zeitüberschreitung", "WLAN: Gäste-WLAN in Klinik Süd Cafeteria nicht erreichbar", "Neuer Monitor für Buchhaltung").
2. Triage three tickets: set priority per the knowledge article "First Level: Priorität festlegen (Triage)", route each to the right team, assign the Buchhaltung monitor ticket to yourself and start it.
3. Open "Drucker: Etikettendrucker Labor druckt Probenetiketten verschoben": read the history, add a public comment and an internal note, and check whether a Known Error is shown for it.
4. Search the knowledge base for "Etikettendrucker" and use the article in a comment.
5. Resolve one ticket you started (for example "Outlook: Gemeinsames Postfach Einkauf nicht sichtbar") and close a resolved one (for example "Drucker: Netzwerkdrucker Einkauf meldet Tonerwechsel").

Should see: all tickets with internal comments, knowledge articles (employee and internal), assets and the holder's device on a ticket, Known Errors with workaround on a linked ticket.

Must be denied: editing knowledge articles, creating or changing assets, security advisories and findings, role administration, audit log, declaring Major Incidents, publishing briefing items.

Findings to watch: how long triage of 5 tickets takes, whether the queue screen shows enough to route (no queue counts, no saved views).

### WLAN technician (`oliver.stein`, also `nadine.roth`, `jens.albrecht`)

Do:
1. Find "WLAN: Visitenwagen verliert Verbindung auf Station 3B"; open the linked Known Error and read the workaround.
2. Look up the access points (assets of product "Access point AP-310") near Station 3B; open their asset detail and history.
3. Add an internal note with the measurement result and wait on "hardware" for another ticket.
4. Open your team's task ("Firmware-Update der Access Points Klinik Süd vorbereiten") and start it.
5. Create a knowledge article "WLAN: Roaming-Schwellwert prüfen" and publish it.

Should see: tickets of all queues, assets and their locations, Problems, tasks of your team.

Must be denied: security advisories management, role administration, audit log, briefing publishing, changes approval.

### ORBIS technician (`henrik.vogel`, `sandra.winter`, `tobias.kraft`)

Do:
1. Open "ORBIS: Anmeldung am Arbeitsplatz Notaufnahme dauert über 2 Minuten" (urgent, linked to the Major Incident). Read the Major Incident status and add a ticket comment for the reporter.
2. Open the Known Error "ORBIS: Sporadische Zeitüberschreitung im Medikationsmodul" and follow its workaround from the internal article.
3. As `sandra.winter`, take "ORBIS: Neuer Benutzer kann Leistungsanforderung nicht öffnen" (assigned to her) and resolve it with a resolution text.
4. Put "ORBIS: Befund wird im Radiologie-Arbeitsplatz nicht angezeigt" on wait with reason vendor and check that the comment explains why.
5. Check the vendor's task "KIS-Hersteller: Hotfix 2026.3.1 …" (assigned to the vendor team) as a task manager.

Should see: all tickets, Problems, Major Incidents, knowledge, assets.

Must be denied: security finding actions, role administration, infrastructure management (racks) if only `it-specialist`.

### Infrastructure specialist, not primarily on tickets (`uwe.pohl`, also `mirja.engel`)

Do:
1. Start at Infrastructure: open Nord > Haus A > Serverraum > "Rack 1". Check the site tree for all three sites.
2. Open the read-only ticket "Serverraum Nord: Temperaturwarnung Rack 3" (queue Infrastruktur): **can you comment or change it?**
3. Create a Change for a backup window, link affected assets, and submit it. Open the maintenance calendar.
4. Open your task "USV-Test Serverraum Nord durchführen".
5. Look at Services and the impact view.

Should see: infrastructure, changes, services, assets (manage), tickets (read), tasks.

Must be denied: working tickets (comment, assign, resolve) — this is `tickets.view` only; security finding management; role administration.

Findings to watch: how this persona reaches the 3 tickets that concern them without a ticket-centric workflow (no saved views or queue counts; My Work has no tickets).

### Security specialist, not primarily on tickets (`ines.falk`, also `deniz.arslan`)

Do:
1. Open the Security overview, advisories and findings.
2. Read the tickets "Verdacht auf Phishing-Mail an Verwaltung" and "USB-Stick mit Patientendaten auf Station gefunden" (queue Security). Try to comment: this role only has `tickets.view`.
3. Open the audit log, filter for the actions of the last hour, and find the role changes made by the seed.
4. Create a security advisory by hand or accept a risk with reason code and review date.
5. Read the briefing item about the phishing wave.

Should see: security, tickets (read, internal comments), audit log, directory groups, assets and endpoint data, knowledge.

Must be denied: ticket assignment and resolve, role administration, changing assets.

### Vendor (KIS-Hersteller)

Logins `vendor.mueller` and `vendor.schmidt`.

Do:
1. Sign in and look for work: Tasks (the hotfix task for the vendor team) and My tickets.
2. Start and complete the task assigned to the vendor team with a result note.
3. Try to open the Service Desk queue, Assets, Knowledge internal articles, Problems, Briefing and the Overview.
4. Try to open the URL of a ticket that is not yours (copy it from an IT persona).

Should see: only tasks of the vendor team or their own, the public status of Major Incidents, the employee baseline (raise and read own tickets, published employee articles, own assets, notifications).

Must be denied (HTTP 403 or 404 on the backend): any ticket they did not report, queues, assets, internal knowledge, Problems and Known Errors, internal data of the tickets linked to a Major Incident, security, audit, roles, other users' data.

Findings to watch: how the vendor learns what to do (there is no external portal, no invitation, no per-ticket sharing); whether the Overview and navigation show items the vendor cannot open.

### Clinic nurse (`sabine.hartmann` or `jonas.wagner`)

Do:
1. Raise a ticket from a phone-sized or small window: "Visitenwagen verliert Verbindung" with the device (choose your own assigned device).
2. Open My tickets, read the IT's public answer, reply, and see that internal notes are not visible.
3. Search knowledge for "Visitenwagen" and use the employee article.
4. Request a DECT phone from the catalog; see the approval go to the manager.
5. Check My assets and the current Major Incident banner or the notification.

Should see: own tickets and public comments, employee articles, own assets, the catalog, own requests.

Must be denied: other people's tickets, internal comments and queue assignment, staff pages, assets of others.

Findings to watch: time to raise a ticket during a shift, wording of status, whether the device picker lists only own devices.

### Doctor (`katharina.brandt`; also `sven.lindner`, `thomas.krause`)

Do:
1. Raise an urgent ticket for the wristband printer in the Notaufnahme. Check that priority and queue cannot be set as an employee.
2. Open the Major Incident "ORBIS: Anmeldung am Standort Nord gestört" and read the status updates.
3. Subscribe to the Major Incident for notifications.
4. Request "ORBIS-Zugang beantragen" for a colleague (requested for), fill the form, see the status.
5. Approve a pending request as a manager, if one exists (create one with `marcel.voigt`, whose manager is `katharina.brandt`).

Should see: own tickets, public Major Incident information, catalog, approvals assigned to you.

Must be denied: everything in the staff area.

### Administrative staff (`monika.schaefer`, `rainer.becker`, `claudia.neumann`)

Do:
1. `monika.schaefer`: raise "Passwort zurücksetzen" for a colleague (requested for) and see that an employee cannot choose another affected user.
2. `rainer.becker`: request a printer setup and a software item; follow the request to its fulfillment tasks. `rainer.becker` has `monika.schaefer` as manager; `monika.schaefer` has no manager: compare what approval looks like in both cases.
3. `claudia.neumann`: report the phishing mail, read the briefing warning on the Overview, open the guest Wi-Fi and phishing articles.

Should see: own tickets and requests, employee articles, own approvals.

Must be denied: staff pages, other people's tickets and assets.

### IT lead running the 08:00 stand-up (`christian.hoffmann`; observers `silke.brandl`, `martin.kessler`)

Do (time-box 15 minutes with the other two leads in a call, each on their own screen):
1. Open the Overview: which briefing items, open Major Incidents, unassigned tickets and pending approvals are shown?
2. Go through the open Major Incident and its linked tickets; post a public update.
3. Check unassigned and urgent tickets per team; assign two tickets to team members.
4. Review Known Errors and the three Problems; set an owner or plan a resolution.
5. Check team workload: tasks per team, who is available (presence is optional and default off).
6. Publish a new briefing item for the day.
7. Declare a new Major Incident for a simulated outage and link two tickets.
8. Approve or schedule a Change.

Should see: the whole service desk, Major Incidents, Problems, briefing management, changes, requests, team tasks.

Must be denied: role administration, platform settings, module switches, audit log (unless also granted).

Findings to watch: whether 15 minutes suffice with this tooling; absence of queue counts, saved "stand-up" views, per-site filter, agenda or minutes (see the gap list). Record each workaround the leads invent.

### Weekly all-IT meeting (no vendor)

Facilitator: `christian.hoffmann`; participants: one person per team. Walk through Problems and Known Errors, the maintenance calendar and Changes, the Security overview and the open tasks per team. Check that the vendor has no access to any of these pages (sign in as `vendor.mueller` in a second window).

### Administrator (`devadmin`)

Do: open `/admin/roles` and compare the six simulation roles with the tables above; open `/admin/role-assignments`; check the audit log for `authorization.role.*` and `demo.hospital.*`; open `/admin/modules`; verify that no simulation user holds `platform.admin` or `platform.roles.manage`.

## Cross-persona checks

| Check | How |
|---|---|
| No privilege through URL | Copy `/service-desk`, `/assets`, `/security/overview`, `/admin/roles` into the window of a nurse and of the vendor; expect a denied or empty page |
| Ticket visibility | A nurse sees only own tickets; IT sees all; the vendor sees none unless they reported one |
| Internal comments | Never visible to the reporter |
| Audit | Every ticket assignment and role change by testers appears in the audit log with the right actor |
| Notifications | After assigning a ticket, the assignee has an in-app notification; the reporter gets one on resolve |
| Concurrency | Two leads change the same ticket; the second gets a version conflict message, not a silent overwrite |

## How the seed writes organization data

Sites, areas (Locations as a tree below their site, codes `SIM-SITE-*` and `SIM-AREA-*`), Departments, Teams (leaders become Team leads), Roles (created from the Role Templates, with `templateKey` recorded) and role assignments are created through the same audited People and role operations as the UI uses. The persona Users are emergency accounts (so every persona can sign in without a directory); their lifecycle is CLI-only and the People operations refuse them, so their profile attributes (names, email, department, location, manager) are written directly and audited as `demo.hospital.user_profile_seeded`. The seed is idempotent.
