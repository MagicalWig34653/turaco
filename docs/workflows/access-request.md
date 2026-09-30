# Access Request

## Goal
Request and approve access while keeping identity/source-system ownership explicit.

## Flow
1. Catalog Item identifies target Service/system, requested-for User and access role.
2. Eligibility and approval chain are determined by policy/owner.
3. Approved request creates a fulfillment Task or typed integration action; the platform does not invent direct access writes before a target-system connector exists.
4. External system result is recorded with source/external reference and audit.
5. Completion communicates outcome to requester.

Future access reconciliation must distinguish requested/approved intent from externally observed actual membership.
