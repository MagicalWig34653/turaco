# ADR-0024: Outbox Dispatch and Platform Notification Service

- Status: Accepted (2026-10-02, with the F2 design)

## Context

Domain events are written to `platform.outbox_events` in the same transaction as the change (ADR-0006), but nothing delivers them. F2 needs reactions to events (a Task assignment creates a Notification) and F3+ will add more consumers. Notifications must not become a per-module subsystem ([module boundaries](../architecture/module-boundaries.md)).

## Decision

**Outbox dispatch** (`backend/internal/platform/events`):

- A dispatcher in `turaco-worker` polls `platform.outbox_events`, claims one due event at a time (`FOR UPDATE SKIP LOCKED`) inside a transaction and runs the consumers registered for its event type **in that same transaction**. On success the event is marked `processed` and the transaction commits, so a consumer's database writes and the event's completion are atomic.
- Consumers receive the `pgx.Tx`, touch only their own tables and must be idempotent anyway (delivery is at-least-once when a consumer has effects outside the transaction). Consumers never call external systems; they enqueue a job (for example email delivery) in the same transaction.
- On failure the transaction rolls back and a second transaction records `attempts`, `last_error` and a back-off `available_at`. After the maximum number of attempts the event becomes `failed` (terminal, visible in logs and metrics); it is never silently dropped.
- An event type without a registered consumer is marked `processed` (the outbox is also an integration record, not only a work queue).
- No broker is introduced; the dispatcher polls (default 2 s).

**Notification service** (`backend/internal/platform/notifications`):

- Modules emit events or call the service's public contract with a notification *intent* (recipient User, category, i18n key plus parameters, reference to the source record). The service owns Notification, NotificationDelivery and Notification Preference.
- Channels are adapters. F2 provides in-app (a Notification row) and HTML email (SMTP via the standard library `net/smtp` with STARTTLS; no new dependency). Email delivery is a job per NotificationDelivery with retry through the job runner; delivery state is separate from the state of the source record.

## Consequences

- Delivery order is not guaranteed: retries with back-off can deliver a later event of a record before an earlier one, so consumers must derive their effect from current state (for example a `TaskAssigned` consumer compares the event with the task's current assignment) rather than from event order.
- A consumer's time is bounded (`ConsumerTimeout`, default 30 s); a timeout is a recorded failed attempt.
- `processed` outbox rows and notifications are not pruned yet; a retention job is a later obligation.
- One long-lived claim transaction per event: consumers must stay short and database-only.
- A consumer that always fails blocks only its own event after back-off, not the queue.
- Email is at-least-once; the delivery `dedupe_key` prevents duplicates from consumer retries but a crash between SMTP acceptance and the status update can send a mail twice.
- Teams/webhook channels later add adapters without changing producers.
