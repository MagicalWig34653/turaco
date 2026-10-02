# Teams and Email Notifications

Domain modules never send SMTP/Teams messages directly. They emit events/notification intents consumed by the Notification service ([ADR-0024](../decisions/ADR-0024-outbox-dispatch-and-notifications.md)).

## Channels
- In-app (implemented, F2)
- HTML email (implemented, F2; below)
- Teams (workflow/app/bot adapter as chosen by an ADR/integration design; not implemented)
- Webhook (not implemented)

Templates are tenant-brandable and localized. Delivery state/retries are separate from Ticket/Request state.

Teams is a user interaction/notification channel, never the authoritative source for Tickets, Tasks or Approvals.

## Email channel

Only `turaco-worker` sends email; `turaco-api` never talks to the relay. Email is disabled unless `SMTP_HOST` is set (see the generated [configuration reference](../reference/configuration.md): `SMTP_*`, `EMAIL_BASE_URL`, `EMAIL_DEFAULT_LOCALE`).

Flow:

1. A module event (for example `TaskAssigned`) is consumed by the outbox dispatcher, which creates the in-app notification in the claim transaction.
2. When the worker has email enabled and the recipient has not opted out of the category, the same transaction creates a `NotificationDelivery` (`channel=email`, unique per notification) and enqueues one `notifications.email.send` job.
3. The job claims the delivery, re-checks that the recipient is an active User with a primary email address and has not opted out, renders the message, sends it and marks the delivery `delivered`. A recipient who is no longer deliverable or who opted out meanwhile cancels the delivery (`cancelled`).
4. A transient failure (network, 4xx) returns the delivery to `pending` and the job is retried with back-off, at most 5 attempts; a permanent failure (5xx such as an unknown mailbox or bad credentials) or the last failed attempt ends it as `failed`. The in-app notification is unaffected by any email outcome.

Guarantees and limits:

- **At-least-once.** A crash after the relay accepted the message but before the status update sends it again on retry. Redelivery of the same event never creates a second delivery.
- **Transport security.** `SMTP_SECURITY=starttls` (default) refuses to continue when the relay does not offer STARTTLS and never sends credentials or messages before the upgrade; `tls` uses implicit TLS; `none` needs both `SMTP_ALLOW_PLAINTEXT=true` and `APP_ENV=development` (APP_ENV defaults to development, so the explicit opt-in is what protects a production deployment that forgot it). Certificates are always verified (`SMTP_CA_FILE` is added to the system pool). The relay password is read from `SMTP_PASSWORD_FILE` (a deployment secret) and never stored or logged.
- **Content.** Subject and bodies come from server-side templates in English and German (`EMAIL_DEFAULT_LOCALE`; recipients have no language setting yet). The only user-supplied text is a Task title, HTML-escaped; descriptions are never included. Control characters are removed from subjects (header injection), recipient addresses come only from Organization data and are validated.
- **Links** are built from `EMAIL_BASE_URL` and a known target type with a validated id.
- **Who is notified.** Task notifications carry the task title, so they are created only for active Users who hold `tasks.work`, `tasks.view` or `tasks.manage`: being a Team member without a task permission, or having created a task and later losing the permission, produces neither an in-app notification nor an email. An "assigned" notification for the same task is not repeated within one hour (assign/unassign cycles cannot flood an inbox). Titles, names and reasons may not contain control, bidirectional-override or invisible formatting characters (`platform/safetext`).
- **Retention.** Notifications keep the title they were created with; they are not pruned yet, so a User who later loses access to a task still sees the old title in their list (the link then gives 404). A retention job is a later obligation.
- **Preferences.** Users opt out per category (`/api/v1/notifications/preferences`); email is on by default.
- Not implemented: bounce handling, unsubscribe headers, digest emails, per-recipient language, tenant branding, delivery status UI or retention of old deliveries.
