# F12 Turaco AI — Feature Design

**Status:** Draft 2026-10-08; decisions A1–A15 adopted by default (autonomous progress; revisit with the product owner and the customer's data protection review). Slice A-A (backend, read-only) is implemented, see [A-A implementation notes](#a-a-implementation-notes); A-B (assistant panel and administration UI) is implemented; A-C and A-D are not. Target design; [current status](current-status.md) is authoritative for what is implemented. Related: [ADR-0029](../decisions/ADR-0029-turaco-ai.md) (decision, non-negotiable), [ADR-0007](../decisions/ADR-0007-isolated-customer-data-planes.md) (data sent to an external AI Provider leaves the customer's data plane), [ADR-0014](../decisions/ADR-0014-application-level-secret-and-file-encryption.md) (AI Provider credentials are secrets), [glossary](../domain/glossary.md), [module boundaries](../architecture/module-boundaries.md), [F11 design](f11-workforce-presence-design.md) (format reference; Presence data is excluded from AI tools), [F10 design](f10-remote-access-design.md).

## Decisions

- **A1 Platform package, not a module.** `backend/internal/platform/ai` owns the provider port, the tool registry, the conversation runtime, AI Proposals and the AI settings. It owns no business data. Modules contribute AI Tools through a registration contract when their feature is designed; `platform/ai` never imports a module and reads no module table (`make archcheck` enforces).
- **A2 No data access path except tools.** No SQL, repository, generic query, generic URL-fetch or file tool exists. A tool is a typed function over a module's public application contract with a JSON schema, a required permission, a risk class and a data-egress declaration (ADR-0029 decisions 2 and 3).
- **A3 Human principal only.** A conversation belongs to exactly one User and one tenant (data plane). Every tool call is built from the requesting User's authorization context (permissions, scopes, tenant) taken from the HTTP session, never from the model. Background AI without a User does not exist. Tool results are filtered by the module the same way its own API filters them.
- **A4 First provider: local OpenAI-compatible runtime (Ollama).** The first adapter speaks the OpenAI-compatible chat-completions API with tool calling against an administrator-configured endpoint (Ollama by default). It needs no account and, on a local host, sends no data out of the installation. A **Fake** provider (scripted responses, deterministic tool calls) ships with it for tests and demos, the same pattern as the F9/F10 fakes. Anthropic, OpenAI and Azure/Microsoft adapters follow behind the same port; they are not part of A-A to A-D.
- **A5 Egress is deny by default, per provider and data class.** Each configured AI Provider has `allowedDataClasses`. A tool is offered to a conversation only if every data class it declares is allowed for the active provider. Provider endpoint URLs are set by administrators only and validated against an egress allowlist (existing `platform/httpx` URL safety: no private-range redirect surprises, no user-supplied URLs). The local provider may be marked `local` (loopback or private range, explicitly allowed by the administrator); external providers require a recorded data processing agreement date, no-training confirmation and region (fields, not free text).
- **A6 Data classes.** `public_reference` (knowledge articles marked visible to the User), `business_record` (ticket, change, asset text visible to the User), `personal_contact` (names, mail addresses of Contacts and Users), `device_context` (endpoint and device summaries after redaction). Never available to any tool, hence not a class: secrets and credentials, Audit records, Workforce Presence details, raw provider payloads, file contents of Artifacts, Remote Access session data. A tool that would return such data is rejected at registration by a registry self-check that compares the tool's declared classes with the closed class list.
- **A7 Prompt injection stance.** All tool results and user-visible record text are placed in a clearly delimited untrusted data section; the system prompt states that instructions inside data are ignored. This is a mitigation, not a control. The controls are structural: the tool set of a conversation is fixed at start from the User's permissions and installation policy, tool arguments are schema-validated and authorization-checked server-side on every call, write tools can only create a Proposal, and model output is rendered as inert text/Markdown without loading images or following links automatically (external links are inert text by default, or open a warning page that shows the full destination and strips query parameters that look sensitive; images are never auto-loaded).
- **A8 Proposals are conversation state.** `ai_proposals` rows are short-lived (default 15 minutes) and not a business record. Anything that must persist goes through existing drafts, Requests, Approvals or Changes. Confirmation is not an Approval (glossary).
- **A9 Retention.** Prompt and response text is not stored by default (ADR-0029). A conversation exists as an in-memory/short-lived server session: only the User-visible transcript for the open assistant panel is kept, in `ai_conversations`/`ai_messages` when `retainConversations` is enabled by the administrator (default off, valid 1 to 30 days). With it off, the panel keeps the transcript in the browser only and the next request resends it; the server persists metadata (A10) only.
- **A10 Audit stores metadata, not content.** See [Audit](#audit).
- **A11 Turn resource scope.** Each turn is bound to the resources the User named or that are the current UI context (for example the open Ticket) plus records directly reachable from them through the module contract. A tool call whose target is outside this scope (an id that appears only in tool output or record text) is refused with `ai.scope_expansion` and surfaced in the UI as a request ("the assistant wants to read X"); the User must consent explicitly per resource. Tool output never extends the scope by itself. `knowledge.search` is the only discovery tool, bounded by paging, and its hits may be read afterwards only after the User selects them.
- **A12 Server-held conversation state.** The browser sends only the new plain user text and a conversation id. Roles, tool calls and tool results live in a short-lived server-side session (`ai.sessions`, TTL 30 minutes, bound to user id and tenant, opaque random id compared with the authenticated session). Browser-supplied system, assistant or tool messages are not accepted by the API (schema allows no role field); a conversation id used by another User or tenant returns not found. This replaces the earlier "browser resends the transcript" model: with retention off, the server session still exists but is deleted at TTL or logout and contains no audit copy.
- **A13 Field-level DTO allowlists.** Data classes only decide provider eligibility; they do not prove safety. Every tool returns a dedicated output DTO (explicit fields, length caps, redaction markers such as `[redacted]` preserved, never re-expanded). Secrets, credentials, Audit records and Workforce Presence details are absent from every tool DTO type; the registry derives the data classes sent from the DTO's declared fields, and a provider is eligible only if all returned fields' classes are allowed. Tests enumerate DTO fields against a prohibited-path list and fail on additions.
- **A14 Dedicated provider transport.** `platform/httpx` offers no URL safety validator, so `platform/ai` gets its own provider HTTP transport: scheme allowlist (https; http only for a `local` provider), host allowlist from the provider record, and a dial-time check of the resolved IP on every connection (custom `DialContext`, so DNS rebinding cannot change the destination after validation); destinations are pinned per provider; redirects are blocked by default; the response size and timeout are capped. External providers may never resolve to loopback, private, link-local or metadata ranges. A separate explicit `local` policy permits loopback or private ranges only for providers an administrator marked `local` (Ollama), still without redirects.
- **A15 Tenant and audit prerequisites.** The authenticated principal currently carries no tenant and audit has no tenant or `via` column. A-A defines trusted tenant resolution: single-tenant installation default is a fixed installation data plane id from configuration; a multi-data-plane deployment resolves the tenant from the server-side session/membership, never from request fields or model output. A-A adds a forward migration extending the audit table with nullable `via` and `tenant_id` columns (and an AI-assisted marker with proposal id) and extends the audit write contract accordingly, before any AI audit entries exist.

## Scope

In: AI Provider configuration (administrator), conversations with the assistant panel, read AI Tools contributed by modules, AI Proposals for `write` tools, rate and cost caps, audit, a read-only Turaco MCP server.

Out: any `high_impact` execution by AI (deployments, remote access, wipe, role or permission changes, risk acceptance, integration configuration); autonomous or scheduled AI without a User; model training or fine-tuning; embeddings or a vector store of tenant data (a retrieval index would need its own authorization-filtering design and ADR check); Presence data as AI input (W8); cross-User or cross-tenant caches; a generic plugin or agent framework (CLAUDE.md build-vs-integrate rule).

## Provider port

```go
// platform/ai (sketch, not final)
type Provider interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) // messages, tool definitions, limits
    Capabilities() Capabilities // toolCalling, maxContext, local bool
}
```

- Adapters live in `platform/ai/providers/{fake,openaicompat}` and only translate wire formats; vendor types never leave the adapter. The Anthropic, OpenAI and Azure adapters are added the same way later.
- Streaming is not required in A-A (a single response per turn). A-B may add server-sent events if the local runtime makes latency a usability problem; it is a transport change, not a domain change.
- Provider credentials (API key, none for local Ollama) are stored with the ADR-0014 secret mechanism, write-only in the API, never logged and never sent into prompts.
- Every provider call has a timeout, a maximum response size and a maximum number of tool-call iterations per turn (default 6).

## Tool registry

```go
type Tool struct {
    Name        string          // "tickets.summarize", module-prefixed
    Description string          // English, model-facing
    InputSchema json.RawMessage // JSON schema, validated server-side
    Permission  string          // existing permission key; checked per call
    Risk        Risk            // read | write | high_impact
    DataClasses []DataClass     // declared egress
    Handler     func(ctx context.Context, caller Caller, input json.RawMessage) (Result, error)
}
```

- Modules register tools at startup through `platform/ai` (a registry like `platform/permissions` and `platform/notifications/registry.go`). `high_impact` tools cannot be registered with an executing handler; they may only return a deep link to the existing draft, Request or Change form (the only effect is that the User opens a prefilled form in Turaco).
- `Caller` carries the User, the authorization context and the tenant. Handlers call the module's public application contract and return a bounded, typed `Result` (field allowlist, text length caps, paging limit). Handlers never return raw entities.
- Tool results record the freshness and source of externally observed data (Observed provider results stay labeled as provider data, see CLAUDE.md), so the model cannot present them as Turaco-owned facts.
- The registry is generated into a reference (`docs/reference/`, like permissions and events) so tool names, risk classes and data classes are reviewable.

### First read tools (A-A)

| Tool | Owner | Data classes | Notes |
| --- | --- | --- | --- |
| `tickets.summarize` | Service Desk | `business_record`, `personal_contact` | Summarizes one Ticket the User may read: fields, public/internal messages the User may see, recent timeline. Internal notes are included only if the User may see internal notes. |
| `knowledge.search` | Knowledge | `public_reference`, `business_record` | Runs the existing authorization-filtered search/knowledge query (platform search, no parallel index); returns titles, snippets and ids, paged (max 10). |
| `devices.context_summary` | Assets/Endpoints | `device_context` | Device summary through the public contract with redaction applied: no credentials, no remote-access identifiers or session data, no secrets in configuration values, no user logon names beyond what the User may see. Provider observed values carry source and freshness. |

Further tools (assignment explanation, failure correlation, knowledge draft) are added per feature, each with its own design review.

## Conversation runtime

1. `POST` a message. The runtime resolves the active provider, loads the User's tool set (permission filter, then data-class filter), and builds the prompt: fixed system prompt, tool definitions, the transcript (bounded), the new message.
2. Provider returns text or tool calls. For each tool call the runtime validates the schema, re-checks the permission for this User, enforces the per-turn call limit, runs the handler with a timeout and appends a delimited result.
3. A `write` tool call returns a Proposal reference to the model ("proposed, awaiting User confirmation") and to the UI; the model cannot execute it.
4. The loop ends on a text answer, the iteration limit or a cap. The response is returned with the list of tools used (names and record references) so the UI can show provenance.
5. Rate and cost caps: per User requests per minute and per day, per installation tokens per day, a maximum context size, a maximum tool-result size. Exceeding returns a typed error (`ai.rate_limited`, `ai.budget_exceeded`), never a silent truncation. Counters are in PostgreSQL (`ai.usage`), no new infrastructure. Reservation is atomic: before each provider call the runtime reserves the worst-case tokens (input size plus the configured maximum output) with one conditional update against the per-User and installation budgets, rejects the call if the reservation does not fit, sets the provider's max output tokens to the reserved amount, and reconciles with the actual usage afterwards (releasing the difference; a failed call releases fully). Concurrent turns therefore cannot overshoot the cap. The same reservation applies to MCP calls. Cost is estimated from token counts and an administrator-set price per million tokens (0 for local).

## Data model sketch (migration 000058 `ai`)

Schema `ai`, owned by `platform/ai`. Forward migration only.

| Table | Purpose |
| --- | --- |
| `ai.providers` | `id`, `kind` (`fake`, `openai_compatible`; later `anthropic`, `openai`, `azure`), `display_name`, `endpoint_url`, `model`, `local bool`, `allowed_data_classes text[]`, `dpa_recorded_on date null`, `no_training_confirmed bool`, `region`, `secret_ref` (ADR-0014), `enabled`, `price_in_per_mtok`, `price_out_per_mtok`, timestamps, `version`. At most one `enabled` provider per installation in A-A (partial unique index). |
| `ai.settings` | Single row: `enabled` (default false), `retain_conversations`, `retention_days`, per-User and per-installation caps, proposal TTL. |
| `ai.conversations` | Only when retention is on: `id`, `tenant_id`, `user_id`, `provider_id`, `created_at`, `expires_at`. |
| `ai.messages` | Only when retention is on: `conversation_id`, `seq`, `role`, `content`, `expires_at`. Purged by job. |
| `ai.proposals` | `id`, `tenant_id`, `user_id`, `conversation_ref`, `tool_name`, `parameters jsonb`, `parameters_hash`, `target_ref`, `target_version`, `state`, `expires_at`, `confirmed_at`, `executed_at`, `outcome_code`, `version`. |
| `ai.usage` | `tenant_id`, `user_id`, `day` (UTC), `request_count`, `tokens_in`, `tokens_out`, `tokens_reserved`, `estimated_cost_micro`. Counts only. Primary key `(tenant_id, user_id, day)`. |
| `ai.usage_hours` | `tenant_id`, `user_id`, `hour` (UTC, truncated), `request_count`. Backs the per-User requests-per-hour cap. |
| `ai.installation_usage` | `tenant_id`, `day`, `tokens_used`, `tokens_reserved`. Backs the installation tokens-per-day cap; reservations are conditional upserts on this row. |
| `ai.sessions` | A12 server-held conversation state, see below. |

### `ai.sessions` (A12)

| Column | Meaning |
| --- | --- |
| `id uuid` | Internal row id, used as audit target; never given to the browser. |
| `token_hash bytea` (unique) | SHA-256 of the opaque 256-bit random conversation id returned to the browser once. The raw id is never stored. |
| `tenant_id text`, `user_id uuid` | Owner. Every lookup filters by `(token_hash, tenant_id, user_id)`; another User or tenant gets not found. |
| `auth_session_id uuid` | The authentication session that created it. A request from a different authentication session (after logout and login) cannot use it. |
| `provider_id uuid` | Provider the conversation started with; a turn after the provider changed is refused (`ai.provider_changed`). |
| `transcript jsonb` | Server-written messages (`user`, `assistant`, `tool` with the tool call id and name). Bounded in count and bytes. Tool content is stored here only, never in audit. |
| `scope jsonb` | A11 turn resource scope: set of `{type, id}` resources the User named, opened as UI context or consented to. |
| `turn_count int`, `version int` | Optimistic concurrency; a turn updates the transcript with `WHERE version = $n`. |
| `busy_until timestamptz null` | Single-flight guard: a second turn while one runs returns `ai.turn_in_progress`. |
| `created_at`, `last_active_at`, `expires_at` | TTL 30 minutes from the last activity, capped at 8 hours after creation. Rows past `expires_at` are unusable at once and deleted by the `ai.sessions.expire` job. |

Indexes: `(expires_at)` for the purge job, `(user_id)`.

### `ai.conversations` and `ai.messages` (retention, written only when `retain_conversations` is on)

`ai.conversations(id, tenant_id, user_id, provider_id, created_at, expires_at)` and `ai.messages(conversation_id, seq, role, content, expires_at)`; only `user` and `assistant` text is retained, never tool results. Index on `(expires_at)`.

### Audit table extension (A15, same migration 000058)

`platform.audit_events` gets nullable `via text` (`ai` or `mcp`, check constraint), `tenant_id text` and `ai_proposal_id uuid`, plus a partial index on `via`. Existing rows and writers are unchanged (NULL). `audit.Change` and `audit.Entry` gain `Via`, `TenantID` and `ProposalID`; `via` requires a tenant. The authenticated `Principal` gains `TenantID` (resolved by a wrapper from the installation data plane id `TENANT_ID`, never from request fields) and `SessionID`.

- Every tenant-owned row carries the data plane key and queries filter by it (ADR-0007). Proposals are readable only by their creating User.
- `parameters` of a proposal are the exact content shown for confirmation; they are deleted at expiry or a fixed time after a terminal state (default 24 hours), keeping `parameters_hash` and ids for audit correlation.
- Indexes on `(user_id, state)` for proposals and `(expires_at)` for the purge jobs.

## State machine (AI Proposal)

`proposed → confirmed → executed | failed`, `proposed → dismissed`, `proposed → expired`. To be added to `docs/domain/state-machines.md` with A-C.

- `Confirm(proposalId, parametersHash)` requires the creating User, state `proposed`, not expired, hash equal to the stored hash, and a re-check of the tool permission and of the target record version. The transition `proposed → confirmed` is a single conditional update (`WHERE state = 'proposed' AND expires_at > now()`), so a confirmation is single-use and race-safe.
- **Durable claim.** `confirmed` is a claim with `claimed_by`, `lease_expires_at` (default 60 seconds) and an `attempt` counter. A worker or the request that wins the conditional update runs the execution; permissions and target version are re-checked immediately before the module call, not only at confirmation. If the lease expires without a terminal outcome, the recovery job `ai.proposals.recover` re-runs the same idempotent call (maximum 3 attempts) and then marks `failed` with `ai.execution_unknown`; the domain operation must honor the idempotency key (a module tool whose operation cannot is not eligible to be a write tool). Terminal states are final and store `outcome_code`. Tests: crash between claim and execution, concurrent confirms, lease expiry with a late completer, double delivery of the idempotency key.
- Execution calls the same module public contract a manual action would, as the User, with an AI-assisted marker (`via=ai`, proposal id) passed in the request context for audit. If the target changed (version mismatch) the proposal fails with `ai.proposal_stale`; it is never silently rebased.
- Execution is idempotent per proposal id (the module call carries an idempotency key derived from it); a retry after a timeout cannot apply twice.
- Operations are explicit domain operations (`Confirm`, `Dismiss`, `Expire`), no generic `UpdateStatus`.
- Writes that send data outside Turaco (requester-visible comments, email) always show the full content, recipients and visibility in the confirm dialog (ADR-0029).

## Permissions

New keys in the permissions registry (`make docs-check` regenerates the reference):

| Key | Meaning | Elevated |
| --- | --- | --- |
| `ai.use` | Use the assistant; tools additionally require their own permission | no |
| `ai.settings.view` | View provider and policy settings (never secrets) | yes |
| `ai.settings.manage` | Configure providers, data classes, caps, retention (high-impact administrative action) | yes |
| `ai.usage.view` | View aggregated usage and cost | yes |

- A tool never grants more than the User already has: tool permission checks reuse each module's existing permission keys.
- AI Provider configuration changes are audited and require re-authentication or the existing elevated-action mechanism.
- Role defaults: `ai.use` is added to the staff roles that already use the workbench, but only after the installation enabled AI; customers (portal users) never get it in F12.

## Audit

Using `platform/audit` with `via=ai`:

- Stored: User, tenant, provider id and model, tool name, input record ids and counts (for example "ticket id X", "10 results"), data classes sent, outcome code, token counts, duration, proposal id, confirmation id, parametersHash.
- Not stored: prompt text, model response text, tool result content, search queries beyond a hash and length (unless `retainConversations` is on, in which case the content lives in the retention tables with its own expiry, not in audit).
- Provider configuration changes: before/after with the secret reference only, never the secret.
- Executed proposals: the module's own audit entry for the business action, marked AI-assisted, plus the AI entries for proposal, confirmation and outcome.
- Denied calls (permission, data class, caps) are audited with the reason code.

## Events and jobs

- Events (registry, versioned): `AIProviderChanged` (A-A); `AIProposalConfirmed`, `AIProposalExecuted`, `AIProposalFailed` (A-C). Payloads carry ids only. The assistant panel does not need events for the conversation itself.
- Jobs (existing `platform/jobs`): `ai.sessions.expire` (every 5 minutes, deletes expired sessions; A-A), `ai.retention.purge` (daily, expired conversations and messages and usage rows older than 35 days, one audit summary with counts; A-A), `ai.proposals.expire` (every minute, expires and purges parameters; A-C).
- AI does not create notifications by itself. A proposal result may use the existing notification service for the creating User only if the product owner later asks for it.

## HTTP API

All routes live under `/api/v1/ai` and require `ai.use` unless noted; AI disabled returns `ai.disabled` with no other data.

- `GET /api/ai/status`: enabled, active provider display name, `local`, allowed data classes, caps remaining (no secrets).
- `POST /api/ai/conversations/messages`: send a message (with the transcript when retention is off); returns the answer, the tools used (names, record references) and any proposals.
- `GET /api/ai/proposals/{id}`, `POST /api/ai/proposals/{id}/confirm` (body: `parametersHash`), `POST /api/ai/proposals/{id}/dismiss`.
- `GET|PUT /api/ai/providers`, `POST /api/ai/providers/{id}/test` (`ai.settings.*`); `GET|PUT /api/ai/settings`; `GET /api/ai/usage` (`ai.usage.view`).
- The OpenAPI/contract entries follow the existing conventions. Error codes are typed (`ai.disabled`, `ai.rate_limited`, `ai.budget_exceeded`, `ai.provider_unavailable`, `ai.proposal_expired`, `ai.proposal_stale`).

## UI

Functional, visual design later (project decision).

- **Assistant panel:** a docked side panel opened from the shell; message list, input, indicator of the active provider and whether it is local, per-answer "used" list (tool and record links), usage remaining. Untrusted output is rendered as inert text/Markdown: no image auto-loading, external links inert or via a destination-showing warning step. If the context is a Ticket or Device, the panel offers "summarize this" as a prefilled message, not an implicit data push.
- **Proposal confirm dialog:** shows the tool, the target record with version, the complete exact parameters and, for outbound content, the full text, recipients and visibility, a hash-bound Confirm button, Dismiss, and the remaining time. No "confirm all". After confirmation it shows the real outcome returned by the module.
- **Administration:** providers (test connection, data classes, DPA fields), policy, caps, retention, usage. All strings via i18n resources; no hard-coded German.

## Turaco MCP server (A-D)

- Transport over the same tool registry; no second tool set. Read tools only until writes can be confirmed out-of-band inside Turaco (a proposal created via MCP is confirmed in the Turaco UI only).
- Per-User OAuth 2.x with audience-bound tokens (audience = the Turaco MCP resource), short lifetime, no static API keys, no token passthrough to other services, server-side authorization of every call by the same `Caller` construction as in the web runtime. Calls are audited with `via=mcp` plus client id.
- Client registration is an administrator allowlist (see open decisions). Rate and cost caps apply as for the assistant.

## Slices

| Slice | Content |
| --- | --- |
| A-A | Backend read-only: `platform/ai` package, provider port with Fake and OpenAI-compatible adapter, tool registry and generated reference, conversation runtime, caps and usage, settings and provider admin API, audit, migration 000058 (providers, settings, usage, sessions; retention tables behind the setting) plus an audit table extension for `tenant_id`/`via` (A15), permissions, tools `tickets.summarize`, `knowledge.search`, `devices.context_summary`, tests including adversarial injection and authorization cases. |
| A-B | Frontend: assistant panel, administration screens, i18n, "summarize this" entry points on Ticket and Device. |
| A-C | Writes: `ai.proposals`, state machine, confirm dialog, first `write` tool (for example a Ticket internal-note draft or a reply draft; chosen with the Service Desk owner), `high_impact` deep-link-only rule, state-machine docs. |
| A-D | Read-only Turaco MCP server, OAuth audience binding, client registration, audit `via=mcp`. |

Later providers (Anthropic, OpenAI, Azure/Microsoft) are separate small slices after the data protection review.

## Reused concepts

Permissions registry and role evaluation, `platform/audit`, `platform/jobs`, event registry, `platform/httpx` URL safety, ADR-0014 secret storage, module public contracts, platform search, i18n resources, the existing elevated-action mechanism for administrative changes.

## New concepts

AI Provider, AI Tool, AI Proposal (already in the glossary), data class (egress class), conversation (transient). Nothing else: no AI task system, no AI notification channel, no AI search index, no AI-specific permission engine.

## Open decisions (proposed defaults, need the product owner's choice)

1. **First provider.** Default: local OpenAI-compatible (Ollama) plus Fake (A4). Alternative: Anthropic first for quality, which needs a data processing agreement and external egress approval.
2. **Egress default.** Default: AI disabled, no provider enabled, no data class allowed; the first enabled provider starts with `public_reference` only and the administrator adds classes explicitly. A `local` provider may start with `business_record` if the administrator sets it.
3. **Conversation retention.** Default: off; when on, 7 days, maximum 30 (A9). Audit never holds content.
4. **MCP client registration.** Default: administrator-managed allowlist of OAuth clients with fixed redirect URIs; dynamic client registration is not enabled.
5. **Proposal TTL.** Default 15 minutes; single-use.
6. **Caps.** Default per User 30 requests/hour and 200/day, per installation 2 million tokens/day, 6 tool iterations per turn; all adjustable.
7. **First write tool.** Default: a draft internal note on a Ticket (low external effect); outbound customer replies only after the full-content confirm dialog is reviewed.
8. **Portal users.** Default: no AI for customers in F12.

## Review-risk list

- **Authorization bypass through the model:** a handler trusting model-supplied user or tenant ids. Mitigation: `Caller` only from the session; tests that a crafted tool argument cannot widen scope.
- **Prompt injection:** ticket or mail text instructing the model to call other tools or exfiltrate. Mitigation: fixed tool set, read-only results, no outbound tools without Proposal, inert rendering; adversarial test fixtures with injected instructions.
- **Data leaving the installation:** a tool added later declaring a wrong data class. Mitigation: closed class list, generated reference, review checklist item, per-class provider allowlist enforced centrally, not in the tool.
- **Secrets, Audit or Presence in results:** field allowlists per tool and a test that scans tool schemas and results for forbidden field names.
- **Cross-User cache or summary leakage:** no shared cache; any future cache keyed by User and tenant and authorization-filtered.
- **Proposal replay or race:** hash-bound, single conditional update, idempotent execution, version check, tests with concurrent confirms.
- **Confirmation fatigue and misleading dialogs:** full exact content, no truncation, no model-written "summary" in place of parameters.
- **Cost runaway:** per-turn iteration limit, caps in PostgreSQL, tool-result size limits.
- **SSRF through provider URL:** administrator-only, egress allowlist, no redirects to private ranges unless the provider is marked `local`.
- **Logging of prompts:** debug logging must not include message content; log tests as for other secret-bearing paths.
- **Local runtime quality:** small Ollama models can ignore tool schemas; the runtime must treat malformed tool calls as errors with a bounded retry, not execute guesses.
- **Scope creep toward a generic agent framework:** rejected by CLAUDE.md; reviewers check new tools against the registry contract only.

## Documentation obligations

With A-A: `current-status.md` (state), glossary (data class, conversation), module-boundaries (platform/ai ownership), generated permissions/events/tool references, config reference (`AI_ENABLED`, caps), threat model addendum (prompt injection, egress), README of the settings. With A-C: state machines. With A-D: MCP client registration notes. ADR-0029 needs a new ADR only if the decisions above are changed.

## Review outcomes

Independent review 2026-10-08 raised eight issues; all are adopted:

| # | Severity | Issue | Resolution |
| --- | --- | --- | --- |
| 1 | high | Tool output could steer further reads | A11 turn resource scope, user consent for expansion |
| 2 | high | Browser-supplied transcripts could be forged or replayed | A12 server-held session, plain user text only |
| 3 | high | Egress class check does not prove content safety | A13 field-level DTO allowlists, field-derived eligibility, prohibited-path tests |
| 4 | high | `platform/httpx` has no URL safety validator | A14 dedicated transport, dial-time IP check, pinning, no redirects, separate local policy |
| 5 | high | Proposal execution lacked a durable claim | State machine: lease, recovery job, re-check before execution, idempotency key, crash/concurrency tests |
| 6 | medium | Caps racy under concurrency | Atomic worst-case reservation and reconcile, incl. MCP |
| 7 | medium | Markdown link exfiltration | Inert links or warning page, no auto-loaded images |
| 8 | medium | No tenant on principal; audit lacks tenant/`via` | A15 trusted tenant resolution and audit migration in A-A |

A-A scope is extended accordingly: `ai.sessions` table, audit schema change, provider transport and DTO tests join migration 000058 and its companion audit migration.

## A-A implementation notes

Where the implementation made a concrete choice the sketch left open (the sketch above stays the target design):

- **Packages.** `platform/ai` (service, registry, store, usage), `platform/ai/safehttp` (A14 transport), `platform/ai/providers/{fake,openaicompat}`, `platform/ai/transport` (HTTP). Tools live in the owning module's `public` package as `AITools(service)` and are registered in `internal/wiring/ai.go`; the registry self-check fails startup on a bad tool. Tool reference: [docs/reference/ai-tools.md](../reference/ai-tools.md) (generated).
- **Tenant (A15).** `TENANT_ID` (default `default`) is the installation data plane id; `authorization.WithTenant` stamps it on the principal in the API composition root. The principal also carries the sign-in `SessionID`. Audit rows get `via`, `tenant_id`, `ai_proposal_id`; `audit.Record` refuses `via` without a tenant.
- **Secrets.** `ai.providers.secret_ref` names a file in `AI_SECRET_DIR`; the API never accepts or returns key material (ADR-0014 file-secret mechanism of this codebase). The adapter reads it when the provider is built.
- **Resource scope (A11).** Target-bearing tools declare which argument is a record id. A call is allowed only if `(type, id)` is in the conversation scope: records named in `context`, UUIDs the User typed in their own message, and explicit `POST /ai/conversations/scope` consent. Refusals come back to the model as a fixed error and to the UI as `scopeRequests`.
- **Tool schemas.** A closed JSON-schema subset (object, string with `maxLength`, `format: uuid`, integer with bounds, boolean, `additionalProperties: false`) validated for every call; audit stores ids, integers and a hash plus length of free text (`x-audit`).
- **Egress (A13).** The runtime serializes the handler DTO and rejects any leaf not declared in `Tool.Output`; classes are derived from the declared fields. A registry test and `wiring/ai_test.go` scan every path against `ai.ProhibitedPathFragments`.
- **Caps.** `ai.Store.Reserve` is one transaction of conditional upserts (hour, user day, installation day) in a fixed lock order; `Settle` reconciles, a failed call settles with zero.
- **Not in A-A.** Streaming, the Anthropic/OpenAI/Azure adapters, `ai.proposals`, write tools, MCP, role defaults for the AI permissions, audit of `GET` reads of settings.

## A-A review outcomes

Review of the A-A commit raised four issues; all are fixed and tested:

| # | Severity | Issue | Resolution |
| --- | --- | --- | --- |
| 1 | high | A `local` provider could reach any public IP over http | Local policy now allows only loopback and private ranges at every dial (link-local, metadata, CGNAT and public stay blocked); a public host must be an external provider (https, DPA). `ValidateEndpoint` rejects public literal IPs for local. |
| 2 | medium | Settlement trusted reported usage above the reservation; the input estimate was weak | Estimate is one token per two bytes plus headroom. `Settle` records the real usage and returns the overage; the runtime audits it (`usage_above_reservation`), discards the turn with `ai.budget_exceeded` and makes no further call, and the counters make later reservations fail once a cap is reached. |
| 3 | medium | A 2-minute turn lease was not renewed across several provider calls | The lease has an ownership token (`ai.sessions.busy_token`); it is renewed before every provider call, tool call and the final save, and release and save require the token. A lost lease aborts the turn with `ai.turn_in_progress` and cannot overwrite the successor's transcript. |
| 4 | medium | Permissions were a snapshot from HTTP authentication | The runtime reloads the User's effective permissions (role evaluator, injected via `Config.Permissions`) before every tool execution; a revoked tool permission yields `permission_denied`, a revoked `ai.use` ends the turn with `ai.not_permitted`. |
