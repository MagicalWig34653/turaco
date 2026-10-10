# Audit screen

`/admin/audit` (`platform.audit.view`) reads `GET /audit-events`, `GET /audit-events/retention` and, with
`platform.audit.export`, `GET /audit-events/export.csv`.

- The date range is always visible (default last 7 days, quick ranges for 24 hours, 7 and 30 days) and is the
  export range; export needs both ends and at most 92 days (checked client-side, enforced by the server).
- Filters: module, actor kind, system actor, via, action prefix (wins over module, the server refuses both),
  target type and id, actor id, correlation id. `auditFilter.ts` maps the form to the API filter.
- Actors and targets show resolved names with the raw id on hover; a removed entity reads "Removed (suffix)";
  unresolved target types are listed in a hint and rows keep ids. System actors use `audit.systemActor.*` keys.
- The detail drawer compares before and after key by key (`auditModel.ts`, bounded depth and rows) and tolerates a
  missing side; "Show everything from this request" filters by correlation id.
- Export errors: 400 `audit.range_required|range_too_long`, 413 `audit.export_too_large` (with the count when
  sent), 429 `audit.export_rate_limited` (with the Retry-After minutes). The CSV is fetched as a blob.
- Action labels are not translated yet; the raw action key is shown.
