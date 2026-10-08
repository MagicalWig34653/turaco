# Turaco AI frontend

The status read gates all AI surfaces, including the `/admin/ai` route. `ai.admin`
is a frontend aggregate of the status settings/usage flags, not a new backend
permission. Per-action status flags control settings reads, writes and usage.
The requested enabled gate also hides administration while status is disabled;
initial provider/runtime activation therefore requires configuration through the
existing API. No frontend bootstrap bypass is provided.

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

Validation: `npx tsc -b`, `npx eslint src`, `npx vitest run` in frontend; the
model and rendering tests cover status gates, request allowlists, versions, errors
and hostile output. `node src/modules/ai/check-missing-i18n.mjs` scans literal UI
translation references across frontend/src against both catalogs. Dynamic keys
are checked by TypeScript and catalog parity/placeholder tests.
