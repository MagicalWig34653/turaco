// Content remains visible without JavaScript; animation is progressive enhancement.
const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
if (!reducedMotion.matches && "IntersectionObserver" in window) {
  const observer = new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue;
        entry.target.classList.remove("reveal-pending");
        entry.target.classList.add("is-revealed");
        observer.unobserve(entry.target);
      }
    },
    { threshold: 0.08 },
  );
  document.querySelectorAll(".reveal").forEach((element) => {
    element
      .querySelectorAll(
        ".feature-card, .screen-card, .principles-grid > article",
      )
      .forEach((item, index) =>
        item.style.setProperty(
          "--reveal-delay",
          `${Math.min(index, 7) * 55}ms`,
        ),
      );
    element.classList.add("reveal-pending");
    observer.observe(element);
  });
  reducedMotion.addEventListener("change", (event) => {
    if (event.matches) {
      document
        .querySelectorAll(".reveal-pending")
        .forEach((element) => element.classList.remove("reveal-pending"));
      document
        .querySelectorAll(".is-revealed")
        .forEach((element) => element.classList.remove("is-revealed"));
      observer.disconnect();
    }
  });
  const counter = document.querySelector("[data-count]");
  if (counter) {
    const counterObserver = new IntersectionObserver((entries) => {
      if (!entries.some((entry) => entry.isIntersecting)) return;
      counterObserver.disconnect();
      const target = Number(counter.dataset.count);
      const started = performance.now();
      function tick(now) {
        const progress = Math.min((now - started) / 650, 1);
        counter.textContent = String(Math.round(target * progress));
        if (progress < 1 && !reducedMotion.matches) requestAnimationFrame(tick);
        else counter.textContent = String(target);
      }
      requestAnimationFrame(tick);
    });
    counterObserver.observe(counter);
  }
}
