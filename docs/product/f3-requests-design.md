# F3 Products, Catalog and Requests — Feature Design

**Status:** Accepted 2026-10-02. Target design; [current status](current-status.md) is authoritative for what exists. Related: [ADR-0025](../decisions/ADR-0025-catalog-form-definitions.md), [F2 design](f2-work-foundation-design.md), workflows ([hardware](../workflows/hardware-request.md), [new workplace](../workflows/new-workplace.md), [software](../workflows/software-request.md), [access](../workflows/access-request.md)).

## Scope decisions

- **D1 Fulfillment is Tasks.** Stock reservation, procurement and asset assignment (F4), software deployment (F6) and access connectors do not exist. F3 fulfills requests with Tasks (typed context `service_request`) and records outcomes; the hooks for later automation are the typed references and the fulfillment templates.
- **D2 Employees need no role.** Browsing the catalog, submitting a request and seeing one's own requests need only a signed-in active User (ownership is the rule, like notifications). IT staff use `requests.view`/`requests.manage`; definitions need `catalog.manage`; products need `products.view`/`products.manage`. Approvers decide the approvals assigned to them (user, or any member of an approver Team) and may read the request they decide.
- **D3 The four workflows are catalog definitions** (ADR-0025). A development seed (`turaco-admin demo seed`) creates sample products, a catalog category and four example items.
- **D4 No draft persistence.** A request is created when submitted; `draft` of the state machine is unused. Product Variants are not implemented (optional in the model, no need yet).
- **D5 Separation of duties.** A User can never decide an approval of their own request or a request made for them.

## 1. Outcome

Employees pick an offering, answer a short form with known context prefilled, and follow the request. Approvers decide in a clear inbox. IT fulfills through shared Tasks and My Work.

## 2. Reused concepts

User, Team, Task/My Work, Notification, Audit, outbox events, permissions, i18n, Directory (manager lookup).

## 3. New concepts

Product/Manufacturer/Category (existing glossary, now implemented), **Catalog Item** (+ definition), **Service Request**, **Approval**, **Request Reference** (typed link request → product/user). Glossary additions: Approval step, Fulfillment template.

## 4. Modules

`products` (existing schema), `catalog`, `requests`, `approvals` (new schema), plus `tasks/public` (a creation contract so other modules can create Tasks with a typed context) and a notification category registry (pays the F2 design debt before more categories are added). Cross-module calls go through `public` contracts; reactions use outbox events.

## 5. Lifecycles

- **Service Request:** `submitted → pending_approval → approved → in_fulfillment ↔ waiting → completed`; terminal alternatives `rejected`, `cancelled`. A request without approval steps goes `submitted → approved` immediately; entering `in_fulfillment` creates the fulfillment tasks. Completion is automatic when all mandatory tasks are completed and optional tasks are completed or cancelled; a manager can complete manually when tasks are in a terminal state (a cancelled mandatory task needs a reason). `waiting` carries a reason (`stock`, `supplier`, `requester`, `external_system`).
- **Approval:** `pending → approved | rejected | cancelled`; decisions are immutable; steps are sequential; a rejection rejects the request; `expired` is not implemented.
- **Catalog Item:** `active ↔ inactive` (inactive items are not offered; existing requests keep their snapshot).

## 6. Data

New schemas: `catalog`, `requests`, `approvals`. `products.*` gets uniqueness and constraints. `requests.service_requests` (human reference `REQ-YYYY-NNNNNN` from a sequence), `requests.request_references`, `requests.request_tasks` (which tasks belong to which request and whether mandatory), `approvals.approvals`, `catalog.items` (definition jsonb + version).

## 7. API

`/products`, `/manufacturers`, `/product-categories`; `/catalog-items` (employee read of active items with resolved options; admin CRUD with `activate`/`deactivate`); `/service-requests` (submit, list own, detail, `cancel`, `waiting`, `resume`, `complete`); `/approvals` (inbox), `/approvals/{id}/approve|reject`.

## 8. Permissions and audit

New: `products.view`, `products.manage`, `catalog.manage`, `requests.view`, `requests.manage`. Every mutation is audited (titles/answers are not copied into audit; ids and field keys are). Events: `ServiceRequestSubmitted` (existing), `ApprovalRequested`, `ApprovalDecided`, `ServiceRequestApproved`, `ServiceRequestRejected`, `ServiceRequestCompleted`, `TaskCancelled`.

## 9. Notifications

Categories `approval.requested` (approvers), `request.approved`, `request.rejected`, `request.completed` (requester); email follows the F2 channel. Pending approvals also appear in the approvals inbox; merging them into My Work is future work.

## 10. Security and privacy

Answers can contain personal data: visible only to the requester, requested-for User, approvers of that request and `requests.view`. Input is validated against the definition snapshot (types, limits, references must exist and be active, product choices restricted to the definition); text goes through `platform/safetext`. Approver resolution and the self-approval rule are enforced in the backend inside the decision transaction.

## 11. Not implemented in F3

Eligibility/visibility rules per item, cost-based or conditional approvals, approval expiry/delegation, product variants, stock reservation, procurement, asset assignment, software deployment, access connectors, request search beyond list filters.

## 12. Slices

0. Notification category registry. 1. Products. 2. Tasks creation contract and task context. 3. Approvals. 4. Catalog. 5. Requests. 6. UI (catalog admin, employee catalog and request form, my requests, approval inbox, request management). 7. Demo seed and example definitions. Then reviews.
