# Public Website Development

The public website introduces Turaco and renders this repository's documentation. It is independent of the authenticated product frontend and backend. [ADR-0031](../decisions/ADR-0031-public-website-static-site.md) records the decision.

## Structure

- `site/index.html`: landing page with fictional product illustrations.
- `site/assets/`: shared styles, browser scripts and SVG favicon.
- `site/build/`: static generator, pure-function tests and local HTTP server.
- `site/package.json` and `site/package-lock.json`: isolated, pinned development dependency.
- `site/dist/`: generated output, excluded from version control.
- Repository `README.md` and `docs/**/*.md`: documentation source, including generated reference files.
- `.github/workflows/pages.yml`: path-filtered pull-request verification and deployment on `main`.

## Local workflow

Use Node from `.nvmrc`. From the repository root:

```bash
cd site && npm ci && npm test && npm run build && npm run serve
```

The server prints its URL. Alternatively, `make site-build` installs dependencies and builds; `make site-serve` serves an existing build. Any static HTTP server can serve `site/dist/`. Assets and document links are relative so the same files work at `/` locally and `/turaco/` on GitHub Pages. Search needs HTTP access to the generated JSON index.

`npm test` uses `node:test` to verify title extraction, navigation, search indexing and Markdown link rewriting. `npm run build` also validates internal links and fails with diagnostics when a target is missing. Run `make docs-check` after editing source documentation. The website is deliberately excluded from `make check`; its pull-request workflow runs the independent website checks.

## Content and publication review

Use [current implementation status](../product/current-status.md), the [implementation plan](../product/implementation-plan.md) and repository implementation as the evidence for claims. A backend slice does not imply a finished frontend or live integration. Intune and advisory ingestion use fake/import adapters; Autotask has no live REST client. Mark planned capabilities explicitly. Do not invent customers, pricing, downloads, testimonials or performance numbers.

Every product illustration uses fictional data and must be labeled. Keep the visible notice **Project preview — name and branding not final**; see [name clearance](../product/name-clearance.md). The repository currently has no `LICENSE` file: state that fact rather than inventing license terms. Revisit the footer when an explicit license is added.

The builder's allow/deny policy defines which documentation is published. Security policies, architecture and threat-model documentation belong on the public site when they contain normal design guidance. Public development-only credentials in setup instructions are examples, not production secrets. Never publish actual secrets, private customer information, unreleased vulnerability details or incident evidence. Inspect newly included documents; a filename rule alone is not a secrets audit. Excluded documents must not enter generated HTML or the search index, and links to excluded material must be resolved deliberately.

Keep technical documentation in English. Edit the Markdown source rather than generated HTML. Generated reference Markdown is rendered like other documentation; update it through the existing documentation generator when needed.

## Interface rules

Use system fonts and local assets. No external fonts, CDN resources, trackers or cookies. Light, Dark, Cyberpunk and Auto themes share the same markup and persist a local browser preference. Auto follows the operating system. Keep AA text contrast, visible keyboard focus, a skip link, semantic landmarks and usable layouts down to 360 px. Motion must honor `prefers-reduced-motion`, including counters and Cyberpunk effects. Search, mobile navigation, code copying and theme controls must remain keyboard accessible. Print output should show document content without navigation clutter.

## Theme implementation and verification

`site/assets/theme.js` runs in the head before content paints on both landing and generated documentation pages. The native, labeled select offers Light, Dark, Cyberpunk and Auto; `turaco-site-theme` stores the preference with guarded reads/writes. Auto responds to live system color-scheme changes. Storage failures leave the current page usable.

`site/assets/cyberpunk.css` is scoped to Cyberpunk and uses the app's canvas, cyan, magenta and yellow palette. The hero reuses the SVG icon in a decorative neon wordmark above a perspective grid and static scanlines. Solid reading surfaces and bright focus outlines preserve contrast. Decorations are absolute so switching themes does not change content geometry.

Performance budget: two continuous decorative animations (grid transform/opacity and a small border tracer) plus one short wordmark displacement on entry. Glow and shadows stay static; no animated filters, shadows or full-viewport backdrop filters. The mesh and preview frame contain paint. Reduced motion cancels animations; hidden tabs pause them. Keep future effects within this budget.

After changes, run the site tests and build/link check. With cached Playwright Chromium, review landing and documentation screenshots at 390 and 1440 px for all four choices (Auto with both system schemes). Verify keyboard selection, persistence across navigation/reload, blocked storage, system changes, reduced motion, overflow, text contrast and stable content bounds. Browser tooling is local verification only, not a new site dependency.

## Deployment

The owner selects **Settings → Pages → Build and deployment → Source: GitHub Actions**. The workflow uploads only `site/dist/` and deploys to <https://magicalwig34653.github.io/turaco/>. Relevant pushes to `main` and manual dispatch on `main` can deploy. Pull requests run tests and build/link checks only; write permissions are scoped to the deployment job. Non-main manual runs verify the site without publishing it. Pages uses the `pages` concurrency group, while pull-request checks have separate groups.

Action versions follow existing repository release-line pins; see [software supply chain](../security/supply-chain.md). The build never changes repository settings or enables Pages through an API.
