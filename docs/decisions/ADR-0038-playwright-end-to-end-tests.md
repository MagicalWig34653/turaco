# ADR-0038: Playwright for End-to-End Smoke Tests

- Status: Accepted (2026-10-11)

## Context

The frontend has unit and model tests (vitest) but no browser test. Simulation runs with scripted browsers found screen-level defects that unit tests cannot see (crashes on missing fields, hidden controls, broken routes). The repository rule is that a new major dependency needs an ADR.

## Decision

1. **Tool.** `@playwright/test` is a development dependency of `frontend/` only. It is never part of a production image or of the shipped bundle.
2. **Scope.** Smoke tests under `frontend/e2e` drive the real built UI against **mocked API routes** (no database, no backend): sign-in page and methods, route gating, and the screens that crashed in simulation. They check that pages render without console errors and that gated routes show the denied state.
3. **Gate.** `make e2e` runs them; `make e2e-install` installs the browsers. They are not part of `make check`, because browsers are a large download and CI images may not have them. CI may run them in a separate optional job.
4. **Data.** No credentials or real data; every request is fulfilled by route mocks owned by the tests.

## Consequences

- One additional dev dependency with a pinned version in `package-lock.json`.
- Mocked routes can drift from the API contract; the OpenAPI document stays authoritative, and API-level behaviour is covered by Go tests.
- Full-stack end-to-end tests against a real backend remain future work (the multi-agent simulation covers that manually).
