# AI Tool Reference

> Generated from code. Do not edit manually.

Data classes are derived from each tool's output fields (F12 A13); a provider is offered a tool only if it is allowed every class listed. Output fields are the closed allowlist the egress filter enforces. A `write` tool returns no record data to the model: it only creates an AI Proposal that the User confirms in Turaco (F12 A-C); its effect says who can see the result. `high_impact` operations are never registrable.

| Tool | Permission | Risk | Data classes | Target record | Output fields |
|---|---|---|---|---|---|
| `devices.context_summary` | `endpoints.view` | read | `device_context` | yes (`device`, needs the User to have named it) | `name`, `osPlatform`, `osVersion`, `manufacturer`, `model`, `ownership`, `complianceState`, `lastCheckinAt`, `retired`, `linkedToAsset`, `dataOrigin`, `observedSource`, `observedAt`, `lastSyncedAt`, `softwareCount`, `software[].name`, `software[].version`, `software[].publisher`, `openFindingsCount`, `findings[].kind`, `findings[].raisedAt` |
| `knowledge.search` | `knowledge.view` | read | `business_record`, `public_reference` | no | `items[].id`, `items[].reference`, `items[].title`, `items[].snippet`, `items[].audience`, `items[].updatedAt` |
| `tickets.draft_internal_note` | `tickets.manage` | write |  | yes (`ticket`, needs the User to have named it) | none (proposal only, effect `internal`) |
| `tickets.summarize` | `tickets.view` | read | `business_record`, `personal_contact` | yes (`ticket`, needs the User to have named it) | `reference`, `title`, `status`, `priority`, `waitingReason`, `description`, `resolution`, `createdAt`, `updatedAt`, `reporter`, `affectedUser`, `assignee`, `messagesTruncated`, `slaState`, `resolutionDueAt`, `messages[].author`, `messages[].internal`, `messages[].body`, `messages[].createdAt` |
