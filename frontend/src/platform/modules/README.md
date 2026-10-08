# Module switches UI

`/admin/modules` requires `modules.manage`. The registry API supplies the catalog, dependencies,
effective state and optimistic version; toggles send the selected reason and the displayed version.
A conflict preserves its blocker list and requires closing the dialog and reviewing refreshed data.
Core modules have no toggle. Switching off retains data.

`model.ts` is the single frontend route-to-module ownership map. Settings routes are intentionally
exempt so authoritative Presence/AI status permissions can allow configuration while runtime use is
off. `ModulesProvider` reads the signed-in `/modules/status` endpoint, fails closed for unknown
modules, refreshes on focus/every 30 seconds, and refreshes after local configuration changes.
Shell navigation, direct routes, commands, create shortcuts, dashboard cards and embedded feature
cards consume this state. Permissions still apply independently; the backend remains authoritative.

Known backend constraint: the registry currently exempts only `/presence/status` and `/ai/status`
from the API module gate. Settings reads/writes can therefore answer `platform.module_disabled`
when a runtime setting is off. The frontend preserves admin-route access and displays a localized
unavailable view, but cannot bypass this backend restriction to complete initial configuration.

The screen uses shared tables, badges, dialogs, menus and semantic tokens for all three themes;
it introduces no motion. Model tests cover visibility, version zero, blocked switch positions,
filtering, blockers and the complete English/German catalog. Presence/AI model tests cover admin
access while runtime use is disabled.
