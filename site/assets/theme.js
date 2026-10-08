(() => {
  const key = "turaco-site-theme";
  const choices = ["auto", "light", "dark", "cyberpunk"];
  const media = matchMedia("(prefers-color-scheme: dark)");
  let choice = "auto";
  try {
    const saved = localStorage.getItem(key);
    if (choices.includes(saved)) choice = saved;
  } catch {
    /* Storage is optional. */
  }
  function apply() {
    document.documentElement.dataset.theme =
      choice === "auto" ? (media.matches ? "dark" : "light") : choice;
  }
  function visibility() {
    document.documentElement.dataset.visibility = document.hidden
      ? "hidden"
      : "visible";
  }
  visibility();
  document.addEventListener("visibilitychange", visibility);
  apply();
  media.addEventListener("change", apply);
  addEventListener("DOMContentLoaded", () => {
    const selects = document.querySelectorAll("[data-theme-select]");
    selects.forEach((select) => {
      select.value = choice;
      select.addEventListener("change", () => {
        choice = choices.includes(select.value) ? select.value : "auto";
        try {
          localStorage.setItem(key, choice);
        } catch {
          /* Private browsing still works. */
        }
        selects.forEach((other) => {
          other.value = choice;
        });
        apply();
      });
    });
  });
})();
