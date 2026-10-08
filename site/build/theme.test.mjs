import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

const source = readFileSync(
  new URL("../assets/theme.js", import.meta.url),
  "utf8",
);
function boot({ saved, dark = false, blocked = false } = {}) {
  const events = {};
  const select = { addEventListener: (name, fn) => (events.select = fn) };
  const document = {
    hidden: false,
    documentElement: { dataset: {} },
    querySelectorAll: () => [select],
    addEventListener: (name, fn) => (events[name] = fn),
  };
  const media = {
    matches: dark,
    addEventListener: (name, fn) => (events.media = fn),
  };
  const storage = {
    getItem() {
      if (blocked) throw Error("denied");
      return saved;
    },
    setItem(key, value) {
      if (blocked) throw Error("denied");
      saved = value;
    },
  };
  runInNewContext(source, {
    document,
    localStorage: storage,
    matchMedia: () => media,
    addEventListener: (name, fn) => (events[name] = fn),
  });
  // The saved theme is applied before DOMContentLoaded, avoiding a first-paint flash.
  const initial = document.documentElement.dataset.theme;
  events.DOMContentLoaded();
  return {
    document,
    media,
    events,
    select,
    initial,
    saved: () => saved,
    choose(value) {
      select.value = value;
      events.select();
    },
    theme: () => document.documentElement.dataset.theme,
  };
}

test("stored choice applies before content and explicit themes ignore system changes", () => {
  const page = boot({ saved: "cyberpunk" });
  assert.equal(page.initial, "cyberpunk");
  assert.equal(page.select.value, "cyberpunk");
  page.media.matches = true;
  page.events.media();
  assert.equal(page.theme(), "cyberpunk");
  page.choose("light");
  assert.equal(page.saved(), "light");
  assert.equal(page.theme(), "light");
});
test("invalid preferences fall back to Auto and follow live system changes", () => {
  const page = boot({ saved: "invalid", dark: true });
  assert.equal(page.initial, "dark");
  assert.equal(page.select.value, "auto");
  page.media.matches = false;
  page.events.media();
  assert.equal(page.theme(), "light");
  page.choose("dark");
  page.choose("auto");
  assert.equal(page.saved(), "auto");
  assert.equal(page.theme(), "light");
});
test("denied storage does not prevent selecting themes", () => {
  const page = boot({ blocked: true });
  page.choose("cyberpunk");
  assert.equal(page.theme(), "cyberpunk");
});
test("visibility state pauses and resumes decorative motion", () => {
  const page = boot();
  assert.equal(page.document.documentElement.dataset.visibility, "visible");
  page.document.hidden = true;
  page.events.visibilitychange();
  assert.equal(page.document.documentElement.dataset.visibility, "hidden");
  page.document.hidden = false;
  page.events.visibilitychange();
  assert.equal(page.document.documentElement.dataset.visibility, "visible");
});
