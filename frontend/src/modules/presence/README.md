# Presence frontend

Routes: `/presence`, `/presence/teams`, `/admin/presence`. The authenticated
provider reads `/presence/status`, polls every minute and refreshes on window
focus. Failed, missing or disabled status hides navigation, palette entries,
direct screens and embedded hints. Status permissions override session grants
for Presence only. All mutation authorization remains on the backend.

My presence groups server-expanded occurrences into UTC calendar days, shows
local entry times and restricts week/month windows to today or later. The
implementation enforces a rolling 24-hour lookback, so yesterday's midnight
is not a safe query start. Series changes affect the entire entry. Explicit
operations retain the displayed `expectedVersion`; conflicts require closing
the dialog and refreshing, never automatic overwrite. External and cancelled
entries have no mutation actions. The backend currently omits the time zone
of non-recurring all-day entries; the dialog explicitly asks the user to check
the prefilled browser time zone when rescheduling those entries.

Coverage uses server counts and states, never derives individual statistics.
Minimum managers can edit without availability read access. Directory pickers
and location names additionally require `organization.view`. No user details,
absence reason, personal history export, or source configuration is introduced.

Availability hints are batched in the Ticket assignee picker and shown in the
My Work header. They carry explanation, source and observation freshness, use
unknown for failed/pending reads, refresh every minute, and never block assigning.

Settings expose retention, runtime enablement, recorded DPIA/council dates and
a separately confirmed destructive purge. Following the requirement to hide
_everything_ when disabled, re-enabling a disabled installation requires the
admin API; the UI describes this before disabling. Existing external-source
settings are preserved. Purge has no version parameter in the API contract.

Shared dialogs, tables, menus and semantic tokens support Turaco, Dark,
Cyberpunk and reduced motion. No new animation is introduced. English and German
strings live in the platform catalogs. `model.test.ts` covers privacy payloads,
versioned mutations, action visibility, permission-filtered routes/nav/palette,
calendar boundaries, DST, and server-owned recurrence expansion.
