# Presence external sources: threat model and data protection review checklist (F11 P-C)

Status: **design only** (2026-10-12). Nothing of P-C is implemented; this document is the precondition the F11 design names for it. Product decision (2026-10-12): the first external sources are **Microsoft 365 calendar (Graph)**, **manual entry in Turaco** (already built) and, **if technically possible, on-premises Exchange calendar**. No HR/dienstplan import is planned for now.

## 1. What is read and what is never stored

Turaco needs one fact per person and time interval: *unavailable* (and, where the source says so, a work location). It never needs the reason.

| Source | Read | Never read or stored |
| --- | --- | --- |
| Microsoft 365 (Graph) | free/busy status of the person's calendar (`busy`, `oof`, `workingElsewhere`), the automatic-reply *state* and period, the working-hours/work-location signal | event subject, body, location text, attendees, categories, private flags, message content, the reason for an out-of-office, the text of the automatic reply |
| On-premises Exchange (EWS `GetUserAvailability`, `GetUserOofSettings` state and period only) | merged free/busy view of the person for the sync window; OOF enabled flag and period | same as above; `GetUserOofSettings` returns the reply text, which the adapter must discard before it leaves the adapter |
| Manual (Turaco) | a Presence Entry the person or a delegated manager creates | no reason field exists |

Only the intervals mapped to `unavailable` or a work location are stored, with `source`, `source_ref` and `observed_at`. Anything else in a provider response is dropped inside the adapter (tests with reason-bearing fixtures assert that nothing reaches the database, logs or audit).

## 2. Interface choices to verify against a lab before building

- **Microsoft 365:** application permissions are broad. Prefer a **resource-specific / least privilege** setup: `Schedule.Read.All` (free/busy via `calendar/getSchedule`) and `MailboxSettings.Read` (automatic-reply state), restricted to the covered users with an **Application Access Policy** (Exchange Online) so the app cannot read other mailboxes. `Calendars.Read` is **not** acceptable because it returns event subjects. All names and the policy mechanism must be re-checked against current Graph documentation in the lab tenant before implementation.
- **On-premises Exchange:** EWS is the only remote interface that offers availability (REST/Graph do not exist on-premises). It needs a service account with a **narrow impersonation or delegate scope** for the covered mailboxes (`ApplicationImpersonation` is broad; a management-scope-limited role assignment is the minimum). The connector runs inside the customer network (Connector Agent trust boundary, see [agent boundaries](agent-boundaries.md)); the platform never holds on-premises credentials outside the connector. Hybrid mailboxes that live in Exchange Online are read through Graph, not EWS. Basic authentication must not be used; if only NTLM/Kerberos is available the connector uses it from inside the network. Whether this is acceptable to the customer is an open question (section 6).

## 3. Threats (STRIDE-oriented)

| # | Threat | Impact | Mitigation |
| --- | --- | --- | --- |
| P1 | The adapter or a test double stores event subjects or OOF reasons ("sick", "vacation") | health data leak (Art. 9 GDPR) | allow-list mapping inside the adapter, fixture tests with reasons, no raw provider payload in logs or audit, database has no free-text column |
| P2 | Over-broad Graph/EWS permission lets a compromised Turaco read all mailboxes | mailbox compromise | Application Access Policy or management scope limited to covered users; separate app registration per installation; credentials in ADR-0014 secret storage; permission list checked in the health checklist |
| P3 | Absence data used to evaluate people (statistics per person) | works-council violation | no per-person aggregate endpoint (W8), review rule "no query groups by user over more than a day", retention cap of 30 days |
| P4 | Source identity mapping links the wrong person (shared mailbox, reused address) | wrong absence shown | mapping by directory id or primary address of an **active employee** only, ambiguous matches are skipped and reported, shared/room mailboxes excluded |
| P5 | Stale or replayed data (a vanished vacation stays) | wrong availability | `observed_at` freshness, vanished entries are closed on the next full window, a failed source shows as unknown, never as available |
| P6 | Sync as denial of service or API throttling | provider lockout | per-source advisory lock, bounded batch and window (28 days ahead), backoff on 429, rate limit, run budget |
| P7 | Connector (on-premises) compromised and used as pivot | lateral movement | connector holds only the Exchange service account and an outbound channel; no inbound commands; capability allow-list `presence.observe` only |
| P8 | Enabling without the legal basis | compliance violation | settings refuse external sources until `dpiaRecordedOn` (and, if a works council exists, `councilConfirmedOn`) are recorded (already enforced by the F11 settings rules) |
| P9 | Users do not know what is collected | transparency | the UI shows per person what is visible to whom and which source supplies it; source names appear on entries |

## 4. Data protection review checklist (to be completed by the operator before enabling a source)

- [ ] Purpose documented: operational availability for assignment, change windows and coverage; no performance evaluation.
- [ ] Legal basis recorded (Art. 6 (1) (f) legitimate interest with balancing test, or an agreement with the works council under § 87 (1) no. 6 BetrVG where one exists); `dpiaRecordedOn` entered in the Presence settings.
- [ ] The data categories of section 1 reviewed; confirmed that no reason, subject or message content is processed.
- [ ] Covered population defined (which employees, which Teams); shared and room mailboxes excluded.
- [ ] Retention set to the shortest workable value (at most 30 days) and the purge job verified.
- [ ] Processors named: Microsoft (Graph) under the customer's existing Microsoft 365 DPA; for on-premises Exchange no processor.
- [ ] Access to absence data reviewed: who holds `presence.view_entries`; visibility defaults confirmed ("availability only").
- [ ] Employee information notice updated; the in-product notice reviewed.
- [ ] Rights handled: access and erasure requests can be answered from the Presence entries and the audit log (ids only).
- [ ] Lab verification done: a lab tenant (or lab Exchange) run with reason-bearing test calendars proving that nothing but intervals is stored.

## 5. Implementation order (when approved)

1. `integrations/presence` port `Source{Key, Observe(ctx, since)}` with a Fake and `NotConfigured`, source configuration and run tables, jobs `presence.source_sync`, settings and API of the F11 design (`GET|PUT /presence/sources`).
2. Microsoft 365 adapter behind the port, verified in a lab tenant.
3. On-premises Exchange adapter inside the Connector Agent (needs the connector capability design in [agent boundaries](agent-boundaries.md)) after the customer confirms the interface in section 2.

## 6. Open questions for the customer

1. Is the mailbox population in Exchange Online, on-premises, or hybrid? (decides the adapter set)
2. Is an Application Access Policy (Exchange Online) acceptable, and may a limited EWS service account be created on-premises?
3. Is a works council involved, and has it been informed?
4. Are shared and room mailboxes in scope for any reason?
