# Teams and Email Notifications

Domain modules never send SMTP/Teams messages directly. They emit events/notification intents consumed by the Notification service.

## Channels
- In-app
- HTML email
- Teams (workflow/app/bot adapter as chosen by an ADR/integration design)
- Webhook

Templates are tenant-brandable and localized. Delivery state/retries are separate from Ticket/Request state.

Teams is a user interaction/notification channel, never the authoritative source for Tickets, Tasks or Approvals.
