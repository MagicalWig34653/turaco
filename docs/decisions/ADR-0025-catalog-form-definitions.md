# ADR-0025: Catalog Forms Are a Bounded Typed Schema, Not a Meta-Platform

- Status: Accepted (2026-10-02, with the F3 design)

## Context

Catalog Items need configurable request forms, approval chains and fulfillment steps without a code change per offering. The constitution forbids a generic EAV/meta-platform ("Explicit domain model"), and important relationships of a request (requested Product, User) must be typed references, not hidden JSON.

## Decision

- A Catalog Item stores one **definition** as a validated JSON document: an ordered list of form fields from a **closed set of types** (`text`, `longtext`, `number`, `boolean`, `date`, `select`, `user`, `product`), approval steps and fulfillment task templates. Limits are enforced (field, step and template counts, text lengths, key patterns). There are no expressions, scripts, conditions or custom field types.
- Fields of type `user` and `product` are **typed references**: answers are validated against Organization/Products at submission and additionally stored as rows in `requests.request_references`, so relationships are queryable and keep referential meaning.
- A Service Request stores a **snapshot** of the definition it was submitted against; later edits of the Catalog Item never change in-flight requests.
- Workflows such as hardware, workplace, software and access requests are **definitions** of this schema (documented examples, a development seed), not code paths per workflow. Behavior that the schema cannot express (conditions, cost-based approval, stock reservation) needs a new domain feature and an ADR, not a more general schema.

## Consequences

- The schema is versioned by its code (new field types are a code change with tests and docs).
- Admin-authored labels and texts are data in one language; the UI around them is localized.
- Approvals and fulfillment reuse the shared Approval and Task concepts; Catalog owns only the definition.
