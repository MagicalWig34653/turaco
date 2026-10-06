const root = new URL(document.body.dataset.siteRoot || "./", location.href);
const dialog = document.querySelector(".search-dialog");
const input = document.querySelector("#search-input");
const results = document.querySelector("#search-results");
const status = document.querySelector("#search-status");
let indexPromise;
let request = 0;
const getIndex = () =>
  (indexPromise ||= fetch(new URL("search-index.json", root))
    .then((r) => {
      if (!r.ok) throw new Error("Search unavailable");
      return r.json();
    })
    .catch((error) => {
      indexPromise = undefined;
      throw error;
    }));
function openSearch() {
  dialog.showModal();
  input.focus();
  void search();
}
document
  .querySelector("[data-search-open]")
  .addEventListener("click", openSearch);
document
  .querySelector("[data-search-close]")
  .addEventListener("click", () => dialog.close());
document.addEventListener("keydown", (event) => {
  // Search inputs otherwise consume Escape to clear their value first.
  if (event.key === "Escape" && dialog.open) {
    event.preventDefault();
    dialog.close();
    return;
  }
  const editing = event.target.closest(
    'input,textarea,select,[contenteditable="true"]',
  );
  if (
    ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") ||
    (event.key === "/" && !editing)
  ) {
    event.preventDefault();
    if (!dialog.open) openSearch();
    else input.focus();
  }
});
async function search() {
  const current = ++request;
  const words = input.value.toLowerCase().trim().split(/\s+/).filter(Boolean);
  results.replaceChildren();
  if (!words.length) {
    status.textContent =
      "Type to search. Use Tab to reach results, Escape to close.";
    return;
  }
  status.textContent = "Searching…";
  try {
    const pages = await getIndex();
    if (current !== request) return;
    const matches = pages
      .map((page) => {
        const title = page.title.toLowerCase(),
          headings = page.headings.toLowerCase(),
          text = page.text.toLowerCase();
        const scores = words.map(
          (word) =>
            (title.includes(word) ? 10 : 0) +
            (headings.includes(word) ? 4 : 0) +
            (text.includes(word) ? 1 : 0),
        );
        return {
          page,
          score: scores.every(Boolean) ? scores.reduce((a, b) => a + b, 0) : 0,
        };
      })
      .filter((x) => x.score)
      .sort(
        (a, b) => b.score - a.score || a.page.title.localeCompare(b.page.title),
      );
    status.textContent = matches.length
      ? `${matches.length} matching pages${matches.length > 30 ? "; showing the first 30" : ""}.`
      : "No matching pages. Try a shorter or broader term.";
    for (const { page } of matches.slice(0, 30)) {
      const li = document.createElement("li"),
        a = document.createElement("a"),
        small = document.createElement("small");
      a.href = new URL(page.url, root);
      a.textContent = page.title;
      const start = Math.max(0, page.text.toLowerCase().indexOf(words[0]) - 45);
      small.textContent = `${start ? "…" : ""}${page.text.slice(start, start + 180)}…`;
      a.append(small);
      li.append(a);
      results.append(li);
    }
  } catch {
    if (current === request)
      status.textContent =
        "Search could not load. Check your connection and type again, or use the navigation.";
  }
}
input.addEventListener("input", search);
for (const pre of document.querySelectorAll("pre")) {
  const code = pre.querySelector("code");
  if (!code) continue;
  const button = document.createElement("button");
  button.type = "button";
  button.className = "copy-code";
  button.textContent = "Copy";
  button.setAttribute("aria-label", "Copy code");
  button.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(code.textContent);
      button.textContent = "Copied";
    } catch {
      button.textContent = "Select code to copy";
      const selection = getSelection(),
        range = document.createRange();
      range.selectNodeContents(code);
      selection.removeAllRanges();
      selection.addRange(range);
    }
    setTimeout(() => {
      button.textContent = "Copy";
    }, 2500);
  });
  pre.append(button);
}
const nav = document.querySelector(".mobile-nav");
const mobile = matchMedia("(max-width: 800px)");
function adjustNav() {
  nav.open = !mobile.matches;
}
adjustNav();
mobile.addEventListener("change", adjustNav);
