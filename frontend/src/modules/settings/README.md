# Administration settings (F15 groundwork)

Screen for the runtime settings (`GET /admin/settings`, `PUT /admin/settings/{key}`), routed at `/admin/settings` in
the Administration navigation group.

- Permission: `platform.health.view` shows the route and the values; `platform.admin` enables editing. The backend
  remains authoritative.
- Settings are code-defined in `backend/internal/platform/settings`; the screen groups them by owning module and
  renders typed inputs (checkbox, select, number). Durations are edited in minutes and sent as seconds.
- Every write sends the `expectedVersion` of the loaded setting. A `settings.version_conflict` reloads the list and
  shows a review notice. Settings flagged `notYetActive` are stored but not read by any behavior yet and are labelled so.
- `teams.cards_with_titles` shows a patient-data warning when it is switched on.
