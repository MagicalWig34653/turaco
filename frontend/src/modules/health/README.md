# Health, integrations, system and setup (F14 A-F)

Screens for the platform health contract (`GET /admin/health`, `/admin/integrations`, `/admin/system`,
`/admin/setup`, `PUT /admin/setup/items/{key}`), routed at `/admin/health`, `/admin/integrations`,
`/admin/system` and `/admin/setup` in the Administration navigation group.

- Permission: `platform.health.view` shows the routes and the Overview tile; `platform.admin` enables skip,
  confirm and reopen on the setup checklist. Direct URLs without permission show the shared forbidden view.
  The backend remains authoritative.
- Server state is read with `useAsync` (one request per screen, refresh button, no polling). Setup writes send
  `expectedVersion` of an existing mark; a `health.version_conflict` reloads the list and shows a review notice.
- Status vocabulary (`model.ts`): `ok` is the only success tone; `stale`, `fake` and `not_configured` are warnings,
  `failing` is danger, `disabled` neutral, `unknown` unknown. Brand colors carry no status meaning, and the badge
  always has text and a symbol.
- Checks show the check time (`observedAt`, cached by the server for a few seconds), last success and last attempt,
  the machine error code with a localized explanation, and the next step: an in-app link (only for routes this client
  has; the contract's `/admin/people` and `/admin/directory` map to Users and Directory Sync), the configuration key
  names (never values) and a `docs/` guide link. Other docs paths are not linked.
- `SetupTile` is rendered on the Overview for health viewers while `open > 0`.
- Not built: connectivity probes, per-check history, a worker/jobs tab.
