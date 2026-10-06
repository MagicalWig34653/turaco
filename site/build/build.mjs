import { readdir, readFile, mkdir, writeFile, rm } from "node:fs/promises";
import { existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  escapeHTML as e,
  outputPath,
  titleFromMarkdown,
  navTree,
  renderMarkdown,
  searchEntry,
  checkLinks,
  repoURL,
} from "./lib.mjs";
const site = fileURLToPath(new URL("../", import.meta.url));
const root = path.resolve(site, "..");
// Public documentation only. Security entries below were reviewed as ordinary
// architecture/policy/design documentation, not credentials or private reports.
// New security documents require explicit review and addition to this allowlist.
const publicFolders = new Set([
  "architecture",
  "domain",
  "product",
  "workflows",
  "integrations",
  "decisions",
  "development",
  "operations",
  "reference",
]);
const publicSecurity = new Set([
  "agent-boundaries.md",
  "encryption.md",
  "identity-access-design.md",
  "security-architecture.md",
  "supply-chain.md",
]);
// Defense in depth: never publish private reports, credentials or draft disclosures.
const denied =
  /(?:^|\/)(?:private|internal-only|secrets?|credentials?|vulnerability-reports?|embargoed)(?:[/. -]|$)/i;
async function walk(dir) {
  const entries = await readdir(path.join(root, dir), { withFileTypes: true });
  return (
    await Promise.all(
      entries.map((x) =>
        x.isDirectory() ? walk(`${dir}/${x.name}`) : `${dir}/${x.name}`,
      ),
    )
  )
    .flat()
    .sort();
}
const sources = [
  "README.md",
  ...(await walk("docs")).filter((f) => {
    if (!f.endsWith(".md") || denied.test(f)) return false;
    const [, folder, ...rest] = f.split("/");
    return (
      f === "docs/README.md" ||
      publicFolders.has(folder) ||
      (folder === "security" && publicSecurity.has(rest.join("/")))
    );
  }),
];
const published = new Set(sources);
const pages = [];
for (const source of sources) {
  const markdown = await readFile(path.join(root, source), "utf8");
  // Fail closed for common credential formats. This supplements human review;
  // it is not a replacement for secret scanning or pre-publication review.
  if (
    /-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----|\b(?:gh[pousr]_[A-Za-z0-9]{30,}|AKIA[A-Z0-9]{16})\b/.test(
      markdown,
    )
  )
    throw new Error(`Possible secret in ${source}`);
  let rendered;
  try {
    rendered = renderMarkdown(markdown, source, published, (p) =>
      existsSync(path.join(root, p)),
    );
  } catch (error) {
    throw new Error(`${source}: ${error.message}`, { cause: error });
  }
  pages.push({
    source,
    title: titleFromMarkdown(markdown, source),
    ...rendered,
  });
}
const groups = navTree(pages),
  ordered = groups.flatMap((g) => g.pages);
const relative = (from, to) =>
  path.posix.relative(path.posix.dirname(outputPath(from)), to);
const outputs = new Map();
for (const page of ordered) {
  const rel = (to) => relative(page.source, to);
  const link = (p, label = p.title) =>
    `<a href="${e(rel(outputPath(p.source)))}"${p === page ? ' aria-current="page"' : ""}>${e(label)}</a>`;
  const pos = ordered.indexOf(page);
  const nav = groups
    .map(
      (g) =>
        `<details${g.pages.includes(page) ? " open" : ""}><summary>${e(g.name === "decisions" ? "Decisions / ADRs" : g.name)}</summary><ul>${g.pages.map((p) => `<li>${link(p)}</li>`).join("")}</ul></details>`,
    )
    .join("");
  const toc = page.headings
    .filter((h) => h.depth > 1 && h.depth < 4)
    .map(
      (h) =>
        `<li class="depth-${h.depth}"><a href="#${e(h.id)}">${e(h.text)}</a></li>`,
    )
    .join("");
  const description = `${page.title} — Turaco project documentation. Architecture and plans may describe capabilities not yet implemented.`;
  outputs.set(
    outputPath(page.source),
    `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>${e(page.title)} · Turaco docs</title><meta name="description" content="${e(description)}"><meta property="og:title" content="${e(page.title)} · Turaco"><meta property="og:description" content="${e(description)}"><meta property="og:type" content="article"><meta property="og:url" content="https://magicalwig34653.github.io/turaco/${outputPath(page.source)}"><link rel="canonical" href="https://magicalwig34653.github.io/turaco/${outputPath(page.source)}"><link rel="icon" href="${rel("assets/favicon.svg")}" type="image/svg+xml"><link rel="stylesheet" href="${rel("assets/shared.css")}"><link rel="stylesheet" href="${rel("assets/docs.css")}"><script src="${rel("assets/theme.js")}"></script><script src="${rel("assets/docs.js")}" defer></script></head>
<body data-site-root="${rel("index.html").replace(/index\.html$/, "")}"><a class="skip-link" href="#main">Skip to content</a><header class="docs-header"><a class="brand" href="${rel("index.html")}"><img src="${rel("assets/favicon.svg")}" width="30" height="30" alt="">turaco<span>/ docs</span></a><div class="header-actions"><button type="button" data-search-open>Search <kbd>/</kbd></button><label class="theme-label">Theme <select data-theme-select aria-label="Color theme"><option value="auto">Auto</option><option value="light">Light</option><option value="dark">Dark</option><option value="cyberpunk">Cyberpunk</option></select></label></div></header>
<div class="docs-layout"><aside class="sidebar"><details class="mobile-nav" open><summary>Documentation navigation</summary><nav aria-label="Documentation">${nav}</nav></details></aside><main id="main" tabindex="-1"><nav class="breadcrumbs" aria-label="Breadcrumb"><a href="${rel("index.html")}">Home</a><span>/</span><a href="${rel("docs/README.html")}">Documentation</a><span>/</span><span>${e(page.source.split("/").slice(1, -1).join(" / ") || "Overview")}</span></nav><p class="doc-notice">Project preview — name and branding not final. <a href="${rel("docs/product/current-status.html")}">Check implementation status</a> before treating designs as shipped features.</p><article class="prose">${page.html}</article><a class="edit-link" href="${repoURL}/edit/main/${page.source}">Edit on GitHub ↗</a><nav class="page-turner" aria-label="Previous and next pages">${pos ? link(ordered[pos - 1], `← ${ordered[pos - 1].title}`) : "<span></span>"}${pos < ordered.length - 1 ? link(ordered[pos + 1], `${ordered[pos + 1].title} →`) : ""}</nav><footer class="docs-footer">Built from the repository. No public distribution license granted; LICENSE is not yet committed.</footer></main><aside class="toc"><nav aria-label="On this page"><h2>On this page</h2><ul>${toc}</ul></nav></aside></div>
<dialog class="search-dialog" aria-labelledby="search-title"><div class="search-top"><h2 id="search-title">Search documentation</h2><button type="button" data-search-close aria-label="Close search">✕</button></div><label for="search-input">Titles, headings and content</label><input id="search-input" type="search" placeholder="Try assets, audit, or local setup…" autocomplete="off"><p id="search-status" role="status">Type to search. Use Tab to reach results, Escape to close.</p><ol id="search-results"></ol></dialog></body></html>`,
  );
}
outputs.set("search-index.json", JSON.stringify(pages.map(searchEntry)));
outputs.set(
  "index.html",
  await readFile(path.join(site, "index.html"), "utf8"),
);
for (const name of await readdir(path.join(site, "assets")))
  outputs.set(
    `assets/${name}`,
    await readFile(path.join(site, "assets", name), "utf8"),
  );
outputs.set(".nojekyll", "");
const failures = checkLinks(outputs);
if (failures.length)
  throw new Error(`Broken internal links:\n${failures.join("\n")}`);
const dist = path.join(site, "dist");
await rm(dist, { recursive: true, force: true });
for (const [name, content] of outputs) {
  await mkdir(path.dirname(path.join(dist, name)), { recursive: true });
  await writeFile(path.join(dist, name), content);
}
console.log(
  `Built ${pages.length + 1} HTML pages; all internal links and anchors valid.`,
);
