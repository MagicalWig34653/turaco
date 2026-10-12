# Turaco AI frontend

The status read gates all AI surfaces, including the `/admin/ai` route. `ai.admin`
is a frontend aggregate of the status settings/usage flags, not a new backend
permission. Per-action status flags control settings reads, writes and usage.
Administration remains reachable with the runtime setting off when the always-mounted
status endpoint grants settings/usage permissions. Runtime use additionally requires
`/modules/status` to report AI enabled. The backend module gate exempts the settings,
providers and usage routes while the module is off, so the first configuration can be completed from this screen;
see [module switches](../../platform/modules/README.md).

The authenticated provider refreshes status on focus and every minute. Conversation
state stays in memory across panel closes and is discarded on logout or status
revocation. The native modal dialog docks on the right, contains keyboard focus,
closes on Escape and restores focus. Semantic tokens support Turaco, Dark and
Cyberpunk; there is no new animation, including under reduced motion.

Only a new user message, opaque conversation id and explicitly opened context are
sent. Opening a ticket/device merely prefills a summary draft. Answers and all
transcript content remain inert React text: no Markdown parser, links, images or
HTML interpretation. Tool metadata is displayed separately. Scope requests need
an explicit per-record Allow; successful consent offers a separate retry action.
Ending deletes the active server session; download the plain-text transcript first.

Provider/settings writes use closed payloads with the loaded expectedVersion.
Provider kind is immutable on edit; credentials are represented by secret file
references only. Conflict messages require a reload, never a blind overwrite.

MCP (F12 A-D): the administration screen lists and edits the allowlisted MCP clients
(public clients; redirect URIs entered one per line and sent unchanged for exact
matching) and the "MCP server enabled" setting. `/oauth/authorize` is the consent page:
it reads only the known OAuth query parameters, asks the server to validate them and
leaves only through the redirect URL the approve/deny call returns; an unregistered
client or redirect URI shows an error and never redirects. `/ai/connections` lists and
disconnects the signed-in person's own grants. Both need `ai.use`.

AI Proposals (F12 A-C): a turn may return `proposals`; each is shown inside the panel by
`ProposalCard` as the confirm dialog: operation, visibility (effect), target label, id and
version, every parameter complete as inert text, the remaining time, a Confirm that sends
only `parametersHash` and a Dismiss. There is no "confirm all". After Confirm the card
shows the server's real outcome; on a proposal conflict error it reloads the proposal.

Validation: `npx tsc -b`, `npx eslint src`, `npx vitest run` in frontend; the
model and rendering tests cover status gates, request allowlists, versions, errors
and hostile output. `node src/modules/ai/check-missing-i18n.mjs` scans literal UI
translation references across frontend/src against both catalogs. Dynamic keys
are checked by TypeScript and catalog parity/placeholder tests.
