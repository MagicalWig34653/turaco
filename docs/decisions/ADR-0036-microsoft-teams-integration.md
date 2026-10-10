# ADR-0036: Microsoft Teams as a Notification and Interaction Channel

- Status: Accepted (2026-10-10, product owner decisions in the design). Phase T-A (channel posts) is implemented; T-B to T-E are not. [Current status](../product/current-status.md) is authoritative. Design, phases and threat model: [F15 Microsoft integration, Microsoft Teams](../product/f15-microsoft-integration-design.md#microsoft-teams).

## Context

Turaco notifies in-app and by email through the platform Notification service and the outbox ([ADR-0024](ADR-0024-outbox-dispatch-and-notifications.md), [Teams and email](../integrations/teams-email.md)). IT staff in Microsoft 365 organizations, including hospitals, live in Teams: they want assignments, approvals and Major Incidents there and a place to coordinate an incident. The product owner decided on 2026-10-10 that a Teams integration is added. Teams is cloud-only; on-prem Turaco installations reach it through outbound egress. Ticket titles in a hospital can contain patient information, and members of a Teams channel are outside Turaco's authorization.

## Decision

1. **Teams is a channel, not a source of truth.** Tickets, Tasks, Approvals and Major Incidents stay in Turaco. Teams deliveries are Notification Deliveries of the existing Notification service (new channels `teams` for personal and `teams_channel` for channel posts); no parallel notification, task or approval system is built.
2. **Phases** (each independently shippable, all behind the optional module `teams`, default off):
   - **T-A** outbound posts to channels through Teams Workflows webhook destinations (outbound only).
   - **T-B** personal notifications as Adaptive Cards from a Turaco Teams app with a bot, sent proactively (outbound only) for ticket assigned, approval requested and Major Incident updates, with deep links back to Turaco.
   - **T-C** actionable approve/reject cards. Needs the inbound bot endpoint. The card carries only an opaque single-use action token; the decision runs as the mapped Turaco User through the Approvals contract with full re-authorization. High-impact approvals never get action buttons.
   - **T-D** link a Teams meeting or channel to a Major Incident or a Turaco Team (validated URL as an external reference); optional creation of an online meeting through delegated, use-time consent.
   - **T-E** Microsoft 365 presence is ADR-0028 P-C, not a Teams feature; it shares the Graph read app registration. Teams chat presence stays out of scope (ADR-0028).
3. **Identity.** A Teams user is the Turaco User whose Entra External Identity is `entra:<tid>` + `oid` ([ADR-0035](ADR-0035-entra-oidc-login.md)); the link exists through Entra sign-in or Graph reconciliation. No UPN or email matching; unlinked recipients get no Teams delivery.
4. **Data minimisation by default.** Cards and channel posts carry the category, the reference number and generic wording, never titles, descriptions, names of affected people or comments. An administrator may opt in to titles for personal cards only (recorded DPIA date, audited). Channel posts are always reference-only.
5. **Least privilege.** A separate Teams bot app registration (application permissions only as listed in the consent matrix of the design, resource-specific consent where it exists, admin consent). Inbound bot requests are authenticated by validating Bot Framework JWTs; outbound calls go only to an allow-list of Microsoft hosts and validated webhook hosts, with 429/`Retry-After` handling.
6. **Port.** `backend/internal/integrations/teams` defines the port with `Fake` and `NotConfigured` adapters following the existing provider-port pattern (Intune, Remote Access) and `Mode()` for health.
7. **No new framework.** No Bot Framework SDK (none is maintained for Go); a thin REST client over the shared Microsoft HTTP client and the JWT library of ADR-0035.

## Alternatives considered

- **Office 365 connectors / classic incoming webhooks:** retired by Microsoft. Rejected.
- **Posting to channels through Graph as the application:** Graph allows application-permission channel messages only for migration. Rejected; Workflows webhooks or the bot are the supported paths.
- **Teams activity feed notifications only (Graph `sendActivityNotification`):** outbound only and simple, but no rich card and no actions. Kept as a fallback option for T-B (open question), not the default.
- **Trusting card payloads (approve with ids in the card data):** a forwarded or replayed card would decide as whoever clicks. Rejected; opaque single-use tokens and re-authorization.
- **A separate Teams notification service:** a parallel notification system. Rejected by the constitution.

## Consequences

- Migrations extend the delivery and preference channel lists and add channel routes and action tokens (forward migrations only).
- A new inbound, unauthenticated-by-session HTTP endpoint (T-C) becomes a trust boundary and needs `review-security`.
- On-prem installations need outbound egress (and for T-C an inbound path from the Bot Framework); health and setup show what is missing.
- Category texts gain Teams card templates in English and German.


## Implementation note (2026-10-10)

Slice T-A posts through Workflows webhooks. A Workflows flow cannot deduplicate on a delivery id, so a worker crash between an accepted post and the delivery status update can post the same card twice. This is an accepted operational limitation of webhook channel posts; the card text carries the reference number so a duplicate is harmless. Deliveries themselves are deduplicated by their database key.
