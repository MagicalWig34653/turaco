# ADR-0031: Public website as a separate static site

- Status: Accepted for the project preview (2026-10-06)

## Context

Turaco needs a public introduction and browsable repository documentation. The website must work on GitHub Pages under `/turaco/`, keep the product's dependencies and deployment separate, and describe the early implementation honestly. A server, product login or documentation framework is unnecessary for this scope.

## Decision

- Maintain the landing page, shared CSS/JavaScript and a small Node build script under `site/`, with a separate package manifest and lockfile. Use the repository's Node version.
- Permit one pinned Markdown parser as a development dependency. Use Node's standard library for generation, serving and tests; ship no runtime package dependencies, external fonts, CDN scripts, trackers or cookies.
- Render repository `README.md` and approved `docs/**/*.md` into static HTML, including generated reference Markdown. Source Markdown remains authoritative; the website does not regenerate product reference data.
- Generate folder navigation, page contents, breadcrumbs, adjacent-page links and a local JSON search index during the build. Rewrite document links and fail the build on broken internal links.
- Keep publication allow/deny rules explicit in the build. Normal security policy and design documentation are public; secrets, private incident reports and exploit material are not publishable. Review new documentation before including it.
- Publish only `site/dist/` through GitHub Pages Actions on `main`. Pull requests test and build without deployment permissions. Follow the repository's existing action release-line pinning convention.
- Use shared accessible themes with local preference storage, semantic HTML and reduced-motion support. Product illustrations contain fictional data and are labeled as previews.
- Base capability claims on [current implementation status](../product/current-status.md), separate delivered slices from planned work, and display the [name clearance](../product/name-clearance.md) preview notice. Do not imply a license grant while the repository has no license file.

## Consequences

The public site has no server attack surface or product credentials. A separate dependency tree avoids changes to product package manifests and lockfiles. Static output is portable to a simple HTTP server and the Pages project path.

The small builder owns Markdown links, navigation and search behavior, so it requires focused tests and a build-time link check. Client-side search downloads an index of public documentation; it is not the product's authorization-aware search. Public copy and roadmap status need review whenever implementation truth changes. GitHub Pages must be enabled manually with **GitHub Actions** as the source.

Implementation and maintenance rules are in [website development](../development/website.md).
