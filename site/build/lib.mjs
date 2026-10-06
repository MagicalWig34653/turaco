import path from "node:path";
import { Marked } from "marked";
export const repoURL = "https://github.com/MagicalWig34653/turaco";
export const escapeHTML = (value) =>
  String(value).replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
export function plainText(value, markdown = true) {
  return value
    .replace(/<[^>]*>/g, "")
    .replace(/!?(?:\[([^\]]*)\])\([^)]*\)/g, "$1")
    .replace(/[`*_~]/g, (character) => (markdown ? "" : character))
    .replace(/&amp;/g, "&")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'");
}
export function titleFromMarkdown(markdown, fallback = "Untitled") {
  const heading = new Marked()
    .lexer(markdown)
    .find((t) => t.type === "heading" && t.depth === 1);
  return heading ? plainText(heading.text) : fallback;
}
export const outputPath = (source) => source.replace(/\.md$/i, ".html");
export function headingSlug(text) {
  return plainText(text)
    .toLowerCase()
    .replace(/[^\p{L}\p{N}\s_-]/gu, "")
    .replace(/\s/g, "-");
}
export function navTree(pages) {
  const groups = new Map();
  for (const page of pages) {
    const parts = page.source.split("/");
    const group = parts.length > 2 ? parts.slice(1, -1).join("/") : "overview";
    if (!groups.has(group)) groups.set(group, []);
    groups.get(group).push(page);
  }
  const order = [
    "overview",
    "product",
    "architecture",
    "domain",
    "workflows",
    "integrations",
    "decisions",
    "development",
    "operations",
    "security",
    "reference",
  ];
  return [...groups]
    .sort(
      ([a], [b]) =>
        (order.indexOf(a) < 0 ? 99 : order.indexOf(a)) -
          (order.indexOf(b) < 0 ? 99 : order.indexOf(b)) || a.localeCompare(b),
    )
    .map(([name, items]) => ({
      name,
      pages: items.sort((a, b) => a.source.localeCompare(b.source)),
    }));
}
export function rewriteLink(href, source, published, exists = () => false) {
  if (/^(https?:|mailto:|tel:)/i.test(href) || href.startsWith("//"))
    return href;
  if (/^[a-z][a-z\d+.-]*:/i.test(href)) throw new Error(`Unsafe URL: ${href}`);
  if (href.startsWith("#") || !href) return href;
  const [, pathname, suffix = ""] = href.match(/^([^?#]*)(.*)$/);
  const resolved = path.posix.normalize(
    pathname.startsWith("/")
      ? decodeURIComponent(pathname).slice(1)
      : path.posix.join(
          path.posix.dirname(source),
          decodeURIComponent(pathname),
        ),
  );
  if (resolved.startsWith("../"))
    throw new Error(`Link escapes repository: ${href}`);
  if (published.has(resolved))
    return (
      path.posix.relative(
        path.posix.dirname(outputPath(source)),
        outputPath(resolved),
      ) + suffix
    );
  if (resolved.startsWith("docs/") && /\.md$/i.test(resolved))
    throw new Error(`Unpublished or missing document: ${resolved}`);
  if (!exists(resolved))
    throw new Error(`Missing repository link: ${resolved}`);
  return `${repoURL}/blob/main/${resolved.split("/").map(encodeURIComponent).join("/")}${suffix}`;
}
export function renderMarkdown(markdown, source, published, exists) {
  const headings = [],
    counts = new Map();
  const parser = new Marked({ gfm: true, breaks: false });
  parser.use({
    renderer: {
      heading({ tokens, depth, text }) {
        const base = headingSlug(text),
          count = counts.get(base) || 0;
        counts.set(base, count + 1);
        const id = count ? `${base}-${count}` : base;
        headings.push({ id, depth, text: plainText(text) });
        return `<h${depth} id="${escapeHTML(id)}">${this.parser.parseInline(tokens)}</h${depth}>\n`;
      },
      link({ href, title, tokens }) {
        return `<a href="${escapeHTML(rewriteLink(href, source, published, exists))}"${title ? ` title="${escapeHTML(title)}"` : ""}>${this.parser.parseInline(tokens)}</a>`;
      },
      image({ href, text }) {
        return `<a href="${escapeHTML(rewriteLink(href, source, published, exists))}">${escapeHTML(text || "Image source")}</a>`;
      },
      // Repository Markdown is content, never executable HTML. This also keeps
      // scripts/event handlers out of the public artifact without a sanitizer dependency.
      html({ text }) {
        return escapeHTML(text);
      },
      table(token) {
        return `<div class="table-scroll" role="region" aria-label="Scrollable table" tabindex="0">${Object.getPrototypeOf(this).table.call(this, token)}</div>`;
      },
    },
  });
  return { html: parser.parse(markdown), headings };
}
export function searchEntry(page) {
  return {
    title: page.title,
    url: outputPath(page.source),
    headings: page.headings.map((h) => h.text).join(" "),
    text: plainText(page.html, false).replace(/\s+/g, " ").trim(),
  };
}
export function checkLinks(outputs) {
  const failures = [];
  for (const [file, html] of outputs) {
    if (!file.endsWith(".html")) continue;
    for (const match of html.matchAll(/\b(?:href|src)="([^"]*)"/g)) {
      const href = match[1].replace(/&amp;/g, "&");
      if (/^(?:[a-z][a-z\d+.-]*:|\/\/)/i.test(href)) continue;
      if (href.startsWith("/")) {
        failures.push(
          `${file}: ${href} (root-relative URL breaks project Pages)`,
        );
        continue;
      }
      const url = new URL(href, `https://local.invalid/${file}`);
      const target = decodeURIComponent(url.pathname.slice(1));
      if (!outputs.has(target)) {
        failures.push(`${file}: ${href} (missing ${target})`);
        continue;
      }
      if (url.hash && target.endsWith(".html")) {
        const id = escapeHTML(decodeURIComponent(url.hash.slice(1)));
        if (!outputs.get(target).includes(`id="${id}"`))
          failures.push(`${file}: ${href} (missing anchor)`);
      }
    }
  }
  return failures;
}
