# Turaco public website

The public landing page and documentation are a separate static site. They do not run the Turaco product or require its backend, database or frontend dependencies.

## Build and serve

Use the Node version in the repository's `.nvmrc` (Node 24):

```bash
cd site && npm ci && npm run build && npm run serve
```

The server prints its local address. The build writes `site/dist/`; serve that directory with any simple static HTTP server. Relative assets and document links work both at the server root and under the GitHub Pages `/turaco/` path. Open the generated site through HTTP, rather than `file://`, so search can load its JSON index.

From the repository root, `make site-build` installs the locked site dependency and builds; `make site-serve` serves an existing build. Run `cd site && npm test` for the builder's unit tests. Site checks stay separate from `make check`.

## Publish

In the repository's **Settings → Pages → Build and deployment**, select **GitHub Actions** as the source. The [Pages workflow](../.github/workflows/pages.yml) builds and deploys relevant pushes to `main`; it can also be dispatched manually on `main`. Relevant pull requests run tests and build/link checks with read-only repository permissions and never deploy.

The public URL is <https://magicalwig34653.github.io/turaco/>. Pages configuration is a repository-owner action; the build does not enable Pages through the API.

See [website development](../docs/development/website.md) for publication rules, structure and accessibility requirements, and [ADR-0031](../docs/decisions/ADR-0031-public-website-static-site.md) for the design decision.
