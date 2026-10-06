import test from "node:test";
import assert from "node:assert/strict";
import {
  titleFromMarkdown,
  rewriteLink,
  navTree,
  renderMarkdown,
  searchEntry,
  checkLinks,
} from "./lib.mjs";
const published = new Set([
  "README.md",
  "docs/README.md",
  "docs/domain/glossary.md",
  "docs/product/current-status.md",
]);
test("title extraction ignores code and supports setext and inline markup", () => {
  assert.equal(
    titleFromMarkdown("```md\n# Not a title\n```\n# The **real** `title`"),
    "The real title",
  );
  assert.equal(titleFromMarkdown("Setext title\n============"), "Setext title");
  assert.equal(
    titleFromMarkdown("## Only a subheading", "fallback"),
    "fallback",
  );
});
test("relative markdown links retain queries and anchors at arbitrary depth", () => {
  assert.equal(
    rewriteLink(
      "../domain/glossary.md#user",
      "docs/product/current-status.md",
      published,
    ),
    "../domain/glossary.html#user",
  );
  assert.equal(
    rewriteLink(
      "../../README.md?view=1#status",
      "docs/product/current-status.md",
      published,
    ),
    "../../README.html?view=1#status",
  );
  assert.equal(
    rewriteLink("docs/README.md", "README.md", published),
    "docs/README.html",
  );
  assert.equal(
    rewriteLink("/docs/README.md", "README.md", published),
    "docs/README.html",
  );
  assert.equal(rewriteLink("#local", "README.md", published), "#local");
  assert.equal(
    rewriteLink("https://example.com/a.md", "README.md", published),
    "https://example.com/a.md",
  );
});
test("source links remain in repository; missing/private/unsafe links fail", () => {
  assert.match(
    rewriteLink(
      "../Makefile",
      "docs/README.md",
      published,
      (x) => x === "Makefile",
    ),
    /github.com.*\/Makefile$/,
  );
  assert.throws(
    () => rewriteLink("missing.md", "README.md", published),
    /Missing/,
  );
  assert.throws(
    () =>
      rewriteLink(
        "docs/security/private.md",
        "README.md",
        published,
        () => true,
      ),
    /Unpublished/,
  );
  assert.throws(
    () => rewriteLink("../secret", "README.md", published),
    /escapes/,
  );
  assert.throws(
    () => rewriteLink("javascript:alert(1)", "README.md", published),
    /Unsafe/,
  );
});
test("navigation groups folders, orders predictably and preserves nested paths", () => {
  const pages = [
    { source: "docs/reference/events.md", title: "Events" },
    { source: "docs/product/z.md", title: "Z" },
    { source: "README.md", title: "Home" },
    { source: "docs/product/a.md", title: "A" },
    { source: "docs/domain/nested/a.md", title: "Nested" },
  ];
  const groups = navTree(pages);
  assert.deepEqual(
    groups.map((x) => x.name),
    ["overview", "product", "reference", "domain/nested"],
  );
  assert.deepEqual(
    groups[1].pages.map((x) => x.title),
    ["A", "Z"],
  );
});
test("rendering escapes raw HTML, generates stable duplicate heading anchors and responsive tables", () => {
  const rendered = renderMarkdown(
    "# Title\n## A & B\n## A & B\n<script>alert(1)</script>\n\n| One | Two |\n| --- | --- |\n| a | b |",
    "README.md",
    published,
  );
  assert.deepEqual(
    rendered.headings.map((x) => x.id),
    ["title", "a--b", "a--b-1"],
  );
  assert.ok(!rendered.html.includes("<script>"));
  assert.match(rendered.html, /table-scroll/);
});
test("search index includes readable title, headings and body without HTML", () => {
  const result = searchEntry({
    source: "docs/domain/glossary.md",
    title: "Glossary",
    headings: [{ text: "Assets" }],
    html: "<h1>Glossary</h1>\n<p>Assigned &amp; observed LDAP_URL</p>",
  });
  assert.deepEqual(result, {
    title: "Glossary",
    url: "docs/domain/glossary.html",
    headings: "Assets",
    text: "Glossary Assigned & observed LDAP_URL",
  });
});
test("link checker checks pages, assets and cross-page fragments", () => {
  const output = new Map([
    [
      "index.html",
      '<a href="docs/test.html#valid">Test</a><img src="assets/logo.svg">',
    ],
    [
      "docs/test.html",
      '<h1 id="valid">Valid</h1><a href="../index.html">Home</a>',
    ],
    ["assets/logo.svg", "<svg/>"],
  ]);
  assert.deepEqual(checkLinks(output), []);
  output.set(
    "bad.html",
    '<a href="missing.html">Missing</a><a href="docs/test.html#absent">Absent</a>',
  );
  assert.equal(checkLinks(output).length, 2);
});

test("link checker rejects root-relative assets that break project Pages", () => {
  assert.match(
    checkLinks(
      new Map([
        ["index.html", '<img src="/logo.svg">'],
        ["logo.svg", "<svg/>"],
      ]),
    )[0],
    /root-relative/,
  );
});
