# F12 Turaco AI — Feature Design

**Status:** Draft 2026-10-08; decisions A1–A10 adopted by default (autonomous progress; revisit with the product owner and the customer's data protection review). Nothing is implemented. Target design; [current status](current-status.md) is authoritative for what is implemented. Related: [ADR-0029](../decisions/ADR-0029-turaco-ai.md) (decision, non-negotiable), [ADR-0007](../decisions/ADR-0007-isolated-customer-data-planes.md) (data sent to an external AI Provider leaves the customer's data plane), [ADR-0014](../decisions/ADR-0014-application-level-secret-and-file-encryption.md) (AI Provider credentials are secrets), [glossary](../domain/glossary.md), [module boundaries](../architecture/module-boundaries.md), [F11 design](f11-workforce-presence-design.md) (format reference; Presence data is excluded from AI tools), [F10 design](f10-remote-access-design.md).

## Decisions

- **A1 Platform package, not a module.** `backend/internal/platform/ai` owns the provider port, the tool registry, the conversation runtime, AI Proposals and the AI settings. It owns no business data. Modules contribute AI Tools through a registration contract when their feature is designed; `platform/ai` never imports a module and reads no module table (`make archcheck` enforces).
- **A2 No data access path except tools.** No SQL, repository, generic query, generic URL-fetch or file tool exists. A tool is a typed function over a module's public application contract with a JSON schema, a required permission, a risk class and a data-egress declaration (ADR-0029 decisions 2 and 3).
- **A3 Human principal only.** A conversation belongs to exactly one User and one tenant (data plane). Every tool call is built from the requesting User's authorization context (permissions, scopes, tenant) taken from the HTTP session, never from the model. Background AI without a User does not exist. Tool results are filtered by the module the same way its own API filters them.
- **A4 First provider: local OpenAI-compatible runtime (Ollama).** The first adapter speaks the OpenAI-compatible chat-completions API with tool calling against an administrator-configured endpoint (Ollama by default). It needs no account and, on a local host, sends no data out of the installation. A **Fake** provider (scripted responses, deterministic tool calls) ships with it for tests and demos, the same pattern as the F9/F10 fakes. Anthropic, OpenAI and Azure/Microsoft adapters follow behind the same port; they are not part of A-A to A-D.
- **A5 Egress is deny by default, per provider and data class.** Each configured AI Provider has `allowedDataClasses`. A tool is offered to a conversation only if every data class it declares is allowed for the active provider. Provider endpoint URLs are set by administrators only and validated against an egress allowlist (existing `platform/httpx` URL safety: no private-range redirect surprises, no user-supplied URLs). The local provider may be marked `local` (loopback or private range, explicitly allowed by the administrator); external providers require a recorded data processing agreement date, no-training confirmation and region (fields, not free text).
- **A6 Data classes.** `public_reference` (knowledge articles marked visible to the User), `business_record` (ticket, change, asset text visible to the User), `personal_contact` (names, mail addresses of Contacts and Users), `device_context` (endpoint and device summaries after redaction). Never available to any tool, hence not a class: secrets and credentials, Audit records, Workforce Presence details, raw provider payloads, file contents of Artifacts, Remote Access session data. A tool that would return such data is rejected at registration by a registry self-check that compares the tool's declared classes with the closed class list.
- **A7 Prompt injection stance.** All tool results and user-visible record text are placed in a clearly delimited untrusted data section; the system prompt states that instructions inside data are ignored. This is a mitigation, not a control. The controls are structural: the tool set of a conversation is fixed at start from the User's permissions and installation policy, tool arguments are schema-validated and authorization-checked server-side on every call, write tools can only create a Proposal, and model output is rendered as inert text/Markdown without loading images or following links automatically (links are shown as text with the target, opened only by an explicit click).
- **A8 Proposals are conversation state.** `ai_proposals` rows are short-lived (default 15 minutes) and not a business record. Anything that must persist goes through existing drafts, Requests, Approvals or Changes. Confirmation is not an Approval (glossary).
- **A9 Retention.** Prompt and response text is not stored by default (ADR-0029). A conversation exists as an in-memory/short-lived server session: only the User-visible transcript for the open assistant panel is kept, in `ai_conversations`/`ai_messages` when `retainConversations` is enabled by the administrator (default off, valid 1 to 30 days). With it off, the panel keeps the transcript in the browser only and the next request resends it; the server persists metadata (A10) only.
- **A10 Audit stores metadata, not content.** See [Audit](#audit).

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
5. Rate and cost caps: per User requests per minute and per day, per installation tokens per day, a maximum context size, a maximum tool-result size. Exceeding returns a typed error (`ai.rate_limited`, `ai.budget_exceeded`), never a silent truncation. Counters are in PostgreSQL (`ai_usage`), no new infrastructure. Cost is estimated from token counts and an administrator-set price per million tokens (0 for local).

## Data model sketch (migration 000058 `ai`)

Schema `ai`, owned by `platform/ai`. Forward migration only.

| Table | Purpose |
| --- | --- |
| `ai.providers` | `id`, `kind` (`fake`, `openai_compatible`; later `anthropic`, `openai`, `azure`), `display_name`, `endpoint_url`, `model`, `local bool`, `allowed_data_classes text[]`, `dpa_recorded_on date null`, `no_training_confirmed bool`, `region`, `secret_ref` (ADR-0014), `enabled`, `price_in_per_mtok`, `price_out_per_mtok`, timestamps, `version`. At most one `enabled` provider per installation in A-A (partial unique index). |
| `ai.settings` | Single row: `enabled` (default false), `retain_conversations`, `retention_days`, per-User and per-installation caps, proposal TTL. |
| `ai.conversations` | Only when retention is on: `id`, `tenant_id`, `user_id`, `provider_id`, `created_at`, `expires_at`. |
| `ai.messages` | Only when retention is on: `conversation_id`, `seq`, `role`, `content`, `expires_at`. Purged by job. |
| `ai.proposals` | `id`, `tenant_id`, `user_id`, `conversation_ref`, `tool_name`, `parameters jsonb`, `parameters_hash`, `target_ref`, `target_version`, `state`, `expires_at`, `confirmed_at`, `executed_at`, `outcome_code`, `version`. |
| `ai.usage` | `tenant_id`, `user_id`, `day`, `request_count`, `tokens_in`, `tokens_out`, `estimated_cost`. Counts only. |

- Every tenant-owned row carries the data plane key and queries filter by it (ADR-0007). Proposals are readable only by their creating User.
- `parameters` of a proposal are the exact content shown for confirmation; they are deleted at expiry or a fixed time after a terminal state (default 24 hours), keeping `parameters_hash` and ids for audit correlation.
- Indexes on `(user_id, state)` for proposals and `(expires_at)` for the purge jobs.

## State machine (AI Proposal)

`proposed → confirmed → executed | failed`, `proposed → dismissed`, `proposed → expired`. To be added to `docs/domain/state-machines.md` with A-C.

- `Confirm(proposalId, parametersHash)` requires the creating User, state `proposed`, not expired, hash equal to the stored hash, and a re-check of the tool permission and of the target record version. The transition `proposed → confirmed` is a single conditional update (`WHERE state = 'proposed' AND expires_at > now()`), so a confirmation is single-use and race-safe.
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

- Events (registry, versioned): `ai.proposal.confirmed`, `ai.proposal.executed`, `ai.proposal.failed`, `ai.provider.changed`. Payloads carry ids only. The assistant panel does not need events for the conversation itself.
- Jobs (existing `platform/jobs`): `ai.proposals.expire` (every minute, expires and purges parameters), `ai.retention.purge` (daily, conversations and messages, one audit summary with counts), `ai.usage.roll` (daily housekeeping).
- AI does not create notifications by itself. A proposal result may use the existing notification service for the creating User only if the product owner later asks for it.

## HTTP API

All routes require `ai.use` unless noted; AI disabled returns `ai.disabled` with no other data.

- `GET /api/ai/status`: enabled, active provider display name, `local`, allowed data classes, caps remaining (no secrets).
- `POST /api/ai/conversations/messages`: send a message (with the transcript when retention is off); returns the answer, the tools used (names, record references) and any proposals.
- `GET /api/ai/proposals/{id}`, `POST /api/ai/proposals/{id}/confirm` (body: `parametersHash`), `POST /api/ai/proposals/{id}/dismiss`.
- `GET|PUT /api/ai/providers`, `POST /api/ai/providers/{id}/test` (`ai.settings.*`); `GET|PUT /api/ai/settings`; `GET /api/ai/usage` (`ai.usage.view`).
- The OpenAPI/contract entries follow the existing conventions. Error codes are typed (`ai.disabled`, `ai.rate_limited`, `ai.budget_exceeded`, `ai.provider_unavailable`, `ai.proposal_expired`, `ai.proposal_stale`).

## UI

Functional, visual design later (project decision).

- **Assistant panel:** a docked side panel opened from the shell; message list, input, indicator of the active provider and whether it is local, per-answer "used" list (tool and record links), usage remaining. Untrusted output is rendered as inert text/Markdown without image or link auto-loading. If the context is a Ticket or Device, the panel offers "summarize this" as a prefilled message, not an implicit data push.
- **Proposal confirm dialog:** shows the tool, the target record with version, the complete exact parameters and, for outbound content, the full text, recipients and visibility, a hash-bound Confirm button, Dismiss, and the remaining time. No "confirm all". After confirmation it shows the real outcome returned by the module.
- **Administration:** providers (test connection, data classes, DPA fields), policy, caps, retention, usage. All strings via i18n resources; no hard-coded German.

## Turaco MCP server (A-D)

- Transport over the same tool registry; no second tool set. Read tools only until writes can be confirmed out-of-band inside Turaco (a proposal created via MCP is confirmed in the Turaco UI only).
- Per-User OAuth 2.x with audience-bound tokens (audience = the Turaco MCP resource), short lifetime, no static API keys, no token passthrough to other services, server-side authorization of every call by the same `Caller` construction as in the web runtime. Calls are audited with `via=mcp` plus client id.
- Client registration is an administrator allowlist (see open decisions). Rate and cost caps apply as for the assistant.

## Slices

| Slice | Content |
| --- | --- |
| A-A | Backend read-only: `platform/ai` package, provider port with Fake and OpenAI-compatible adapter, tool registry and generated reference, conversation runtime, caps and usage, settings and provider admin API, audit, migration 000058 (providers, settings, usage; retention tables behind the setting), permissions, tools `tickets.summarize`, `knowledge.search`, `devices.context_summary`, tests including adversarial injection and authorization cases. |
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
