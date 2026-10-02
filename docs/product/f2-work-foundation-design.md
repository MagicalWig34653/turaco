# F2 Work Foundation — Feature Design

**Status:** Accepted 2026-10-02 (decisions W1–W3). Describes the target design; [current status](current-status.md) is authoritative for what is implemented.

Context: [implementation plan](implementation-plan.md), [module boundaries](../architecture/module-boundaries.md), [state machines](../domain/state-machines.md), [Teams/email](../integrations/teams-email.md), [ADR-0024](../decisions/ADR-0024-outbox-dispatch-and-notifications.md).

## Decisions

- **W1:** a new permission `tasks.work` lets a User see and work Tasks assigned to them or to one of their Teams. `tasks.view`/`tasks.manage` stay global (all Tasks); there is no implicit "own tasks" access.
- **W2:** Team assignment needs Teams, so slice 1 includes a small Organization write API for Teams and memberships (platform-sourced; directory-synced data is not overwritten).
- **W3:** outbox dispatch is built first (ADR-0024) because notifications and F3 depend on it.

## 1. Outcome

IT staff get one shared work list (My Work), are notified in the app and by email when work is assigned to them, recurring work is created automatically, and IT can publish manual briefing items.

## 2. Reused concepts

Task, Assignment, My Work, Notification, IT Briefing Item, Scheduled Job, Audit Event, Event; the job runner and schedules (ADR-0006), `audit.Record`, outbox, permission registry, `authorization.Require`, i18n. The existing `platform.tasks` table (migration 000005) is extended by forward migrations only.

## 3. New concepts

- **Recurring Task Definition** — template plus recurrence rule that generates real Tasks; neither a Scheduled Job (technical) nor a Workflow (multi-step).
- **Notification Delivery** and **Notification Preference** — delivery state per channel (already in the state machines) and per-User channel opt-out.
- **Briefing Item (manual)** — an IT Briefing Item authored by a person; later items reference underlying records.

Glossary and state-machine documents are updated with these terms.

## 4. Ownership

`modules/tasks` (Task, Recurring Task Definition), `modules/briefing`, `platform/notifications`, `platform/events` (dispatch). Organization gains Team write operations and a public contract for a User's current Teams and mail address/active status.

## 5. Lifecycle

- **Task:** `open → in_progress | blocked | completed | cancelled`; operations `Create`, `UpdateDetails`, `Assign`, `Unassign`, `Start`, `Block(reason)`, `Unblock`, `Complete`, `Cancel(reason)`, `Reopen(reason)`; no generic status update; a `version` column guards concurrent changes.
- **NotificationDelivery:** `pending → sending → delivered | failed | cancelled`.
- **Briefing Item:** `draft → published → withdrawn`.

## 6. Data

Forward migrations: Task columns (`status_reason` for blocked and cancelled tasks, `created_by_user_id`, `completed_by_user_id`, `version`, `recurrence_definition_id` and `scheduled_for`, unique together so generation is idempotent); `platform.notifications` (unique per recipient and dedupe key), `platform.notification_deliveries` (unique per notification and channel), `platform.notification_preferences`; recurring task definitions; briefing items. Assignment stays as columns on the Task; history lives in audit.

Known limitations (accepted in the slice 2 database review): assignee and Team-membership checks run before the write transaction, so a concurrent deactivation can still produce one assignment or action; deactivating a Team or a User does not touch tasks assigned to them, which stay visible to `tasks.view`/`tasks.manage` (filter by `assignedTeamId`/`assignedUserId`) but drop out of members' My Work; there are no foreign keys from tasks to Organization tables (module boundary, see [module boundaries](../architecture/module-boundaries.md)). Lock order for code touching several owners is task, then team, then user. Security review (2026-10-02) outcomes: task notifications go only to Users holding a task permission; `organization.teams.manage` indirectly controls task scope (documented on the permission); notification titles are retained without a pruning job; membership is resolved outside the write transaction (millisecond TOCTOU window); `CurrentMemberIDs` caps at 500 members.

## 7. API (all lists bounded and paginated)

`/api/v1/tasks[/{id}]` with action endpoints (`assign`, `unassign`, `start`, `block`, `unblock`, `complete`, `cancel`, `reopen`); `/api/v1/my-work`; `/api/v1/notifications` with `read` and preferences; `/api/v1/recurring-task-definitions`; `/api/v1/briefing-items` with `publish`/`withdraw`; Team write endpoints under `/api/v1/teams`.

## 8. Permissions

`tasks.view`, `tasks.manage` (existing, global); new `tasks.work`, `tasks.recurrence.manage`, `briefing.view`, `briefing.manage`, `organization.teams.manage`. Backend-enforced; unknown or foreign Task ids return 404.

## 9. Audit

Every Task operation, Team change, recurrence change and briefing publish/withdraw is audited in the same transaction. Generated Tasks use a system actor. Reading a Notification is not audited.

## 10. Events and background work

New events `TaskAssigned`, `TaskCompleted`, `BriefingItemPublished`. Outbox dispatch (ADR-0024) drives Notifications; email delivery and recurrence generation are jobs. Recurrence is idempotent per `(definition, scheduled run)` and does not catch up missed runs: per definition and pass it creates at most one task, for the oldest due run, and moves the schedule to the first run after now.

## 11. Cross-cutting impact

My Work reads Tasks only in F2 but is a shared query. No full-text search in F2 (list filters only). No new relationship system; `context_type`/`context_id` stay the Task's primary context. Timeline comes from audit. In-app unread count is polled; SSE comes when polling is insufficient.

## 12. External integrations

SMTP via `net/smtp`, configured by environment variables with the password from a secret file; no provider data involved.

## 13. Security and privacy

Header/recipient injection prevented (recipient only from Organization data, headers sanitized); email bodies from server-side templates with escaped parameters, no Task descriptions in mail; IDOR tests for Tasks and Notifications; no mail or new assignments for non-active Users; SMTP secret never logged.

## 14. Tests and documentation

Lifecycle table tests, authorization/IDOR tests, concurrent `Complete`, dispatcher retry/idempotency, recurrence idempotency, fake SMTP; docs: current status, glossary, state machines, data model, generated references, OpenAPI.

## 15. ADR

ADR-0024 (outbox dispatch and notification service). SMTP via the standard library needs none.

## 16. Slices

1. Outbox dispatcher (ADR-0024).
2. Teams write API; Tasks and My Work (backend, then UI).
3. Notifications in-app.
4. HTML email channel.
5. Recurring Task Definitions.
6. Briefing items.
