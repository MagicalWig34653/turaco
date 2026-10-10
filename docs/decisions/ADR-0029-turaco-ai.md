# ADR-0029: Turaco AI — Provider-Independent, Tool-Based and User-Delegated

- Status: Accepted (2026-10-03). The read-only backend slice (F12 A-A) and the assistant panel and administration UI (A-B) are implemented; AI Proposals, write tools and the MCP server are not; see [current status](../product/current-status.md).
- Related: [ADR-0007](ADR-0007-isolated-customer-data-planes.md) (data sent to an external AI Provider leaves the customer's data plane), [ADR-0014](ADR-0014-application-level-secret-and-file-encryption.md) (AI Provider credentials are secrets).
- Not to be confused with [ADR-0018](ADR-0018-ai-model-routing.md), which governs AI assistants used to *develop* Turaco. This ADR is about an AI capability *inside the product*.

## Context

AI assistance can summarize tickets, explain assignments, draft knowledge articles, correlate failures and prepare work. Many model runtimes exist (Anthropic, OpenAI, Azure/Microsoft, GitHub Copilot, local OpenAI-compatible runtimes such as Ollama) and customers differ in which they may use. An AI with broad data access would bypass Turaco's authorization, tenant isolation and audit.

## Decision

1. Turaco AI is a **platform capability** (`platform/ai`): AI Provider Connectors, a tool registry, AI Proposals and a conversation runtime. The domain is not coupled to an LLM vendor; vendor types stay in Connectors. **AI Providers** are configured per Turaco installation by administrators; a local provider is a supported option.
2. AI never receives database access, SQL, repository access, a generic query tool or a generic URL-fetch tool. It acts only through **AI Tools**: explicitly registered, typed operations contributed by modules, each with a schema, required permission, risk class (`read`, `write`, `high_impact`) and a declaration of which data it sends to the AI Provider. Tools call module public application contracts.
3. There is no AI execution without a human principal. Every tool call runs **as the requesting User** with that User's permissions, scopes and tenant (data plane); the AI never holds a broader service identity. Tool results contain only what the User may see. Generated summaries, caches and retrieval indexes are authorization-filtered for whoever views them and never shared across Users or tenants. Prompts never enforce security; tools do.
4. Risk handling:
   - `read` tools may run autonomously within a conversation, bounded by paging limits and rate/cost caps.
   - `write` tools produce an **AI Proposal** (`proposed → confirmed → executed | failed`, or `dismissed | expired`), short-lived conversation state rather than a business record or workflow — anything that must persist goes through existing drafts, Requests, Approvals or Changes: the exact operation, parameters and target record version are stored and shown; the operation executes only after the User confirms. A confirmation is single-use, short-lived and bound to a hash of the exact parameters; permissions are re-checked at execution. It is executed and audited as the User's action, marked as AI-assisted. Confirmation is not an Approval: it is self-confirmation of a delegated action with no separation of duties.
   - `high_impact` operations (deployments, remote access, wipe, role/permission changes, risk acceptance, integration configuration) are never executed by AI. AI can at most prepare a draft or request that enters the existing deterministic Approval/Change/Deployment workflow.
   - Writes that send data outside Turaco (requester-visible comments, email) always show the full content before confirmation.
5. All content from tickets, emails, knowledge, provider data and tool results is untrusted data for the model (prompt injection). Instructions found in data never widen tool access; the tool set of a conversation is fixed by the User's permissions and installation policy. Model output is rendered without automatically loading external resources (images, links).
6. Data egress is an installation decision: administrators enable each AI Provider (prerequisites: data processing agreement, no-training terms, region) and the data classes it may receive. Secrets, credentials, Audit records and Workforce Presence details are never returned by AI Tools. Provider endpoint URLs are set only by administrators and checked against an egress allowlist. Prompt and response retention is off by default. Tool invocations and proposals are audited (User, `via=ai`, provider, model, tool, proposal and confirmation ids, outcome).
7. A future **Turaco MCP server** exposes the same tool registry with the same user-delegated authorization: per-user OAuth with audience-bound tokens, no static API keys, no token passthrough, server-side authorization of every call. It exposes read tools only until writes can be confirmed out-of-band inside Turaco. It is a transport, not a second tool set.

## Consequences

- New platform service in the module boundaries; modules contribute tools when a feature is designed, not up front.
- The first slice is read-only (summaries/explanations over data the User can already read); confirmed writes and MCP follow.
- AI Provider configuration is a high-impact administrative action.
- Which provider ships first, the default egress policy and the MCP client registration model are open decisions for the feature design.
