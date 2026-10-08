# AI Tool Reference

> Generated from code. Do not edit manually.

Data classes are derived from each tool's output fields (F12 A13); a provider is offered a tool only if it is allowed every class listed. Output fields are the closed allowlist the egress filter enforces.

| Tool | Permission | Risk | Data classes | Reads one record | Output fields |
|---|---|---|---|---|---|
| `devices.context_summary` | `endpoints.view` | read | `device_context` | yes (`device`, needs the User to have named it) | `name`, `osPlatform`, `osVersion`, `manufacturer`, `model`, `ownership`, `complianceState`, `lastCheckinAt`, `retired`, `linkedToAsset`, `dataOrigin`, `observedSource`, `observedAt`, `lastSyncedAt`, `softwareCount`, `software[].name`, `software[].version`, `software[].publisher`, `openFindingsCount`, `findings[].kind`, `findings[].raisedAt` |
| `knowledge.search` | `knowledge.view` | read | `business_record`, `public_reference` | no | `items[].id`, `items[].reference`, `items[].title`, `items[].snippet`, `items[].audience`, `items[].updatedAt` |
| `tickets.summarize` | `tickets.view` | read | `business_record`, `personal_contact` | yes (`ticket`, needs the User to have named it) | `reference`, `title`, `status`, `priority`, `waitingReason`, `description`, `resolution`, `createdAt`, `updatedAt`, `reporter`, `affectedUser`, `assignee`, `messagesTruncated`, `messages[].author`, `messages[].internal`, `messages[].body`, `messages[].createdAt` |
