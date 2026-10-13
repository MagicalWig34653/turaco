// Captures the website/README screenshots from a running development stack
// seeded with the hospital simulation (docs/development/simulation-hospital.md).
// Usage (from the repository root, with API + Vite dev server running):
//   node tools/screenshots/capture.mjs [outDir]
// Env: TURACO_URL (default http://localhost:5173), TURACO_USER, TURACO_PASSWORD.
import { chromium } from '../../frontend/node_modules/playwright/index.mjs';

const base = process.env.TURACO_URL ?? 'http://localhost:5173';
const user = process.env.TURACO_USER ?? 'christian.hoffmann';
const password = process.env.TURACO_PASSWORD ?? 'turaco-sim-password';
const outDir = process.argv[2] ?? 'site/assets/screens';

const pages = [
  ['dashboard', '/'],
  ['service-desk', '/service-desk'],
  ['assets', '/assets'],
  ['my-work', '/my-work'],
  ['changes', '/changes'],
  ['infrastructure', '/infrastructure'],
  ['security', '/security/overview'],
  ['knowledge', '/knowledge'],
];

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'en-US', colorScheme: 'light' });
const page = await ctx.newPage();

async function settle() {
  await page.waitForLoadState('networkidle');
  await page.waitForFunction(() => !document.querySelector('[class*="skeleton" i], [aria-busy="true"]'), null, { timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(600);
}

await page.goto(base + '/');
await page.locator('select').selectOption({ label: 'English' }).catch(() => {});
const inputs = page.locator('input');
await inputs.nth(0).fill(user);
await inputs.nth(1).fill(password);
await page.locator('button[type=submit]').click();
await page.waitForLoadState('networkidle');
await page.waitForTimeout(1500);

for (const [name, path] of pages) {
  await page.goto(base + path);
  await settle();
  if (name === 'infrastructure') {
    for (const b of await page.getByRole('button', { name: 'Expand' }).all()) await b.click().catch(() => {});
    await settle();
  }
  await page.screenshot({ path: `${outDir}/${name}.png` });
  console.log(name, page.url());
}

// Ticket detail: pick the ticket with the most content (history) among the first queue entries.
// The optional time-tracking report is not captured; the module is disabled by default.
await page.goto(base + '/service-desk?open=true&assignment=all');
await settle();
const hrefs = await page.locator('main a[href^="/support/"]').evaluateAll((a) => a.map((x) => x.getAttribute('href')).filter((h) => h !== '/support/new').slice(0, 12));
let best = { len: 0, href: null };
for (const href of hrefs) {
  await page.goto(base + href);
  await settle();
  const len = (await page.locator('main').innerText()).length;
  if (len > best.len) best = { len, href };
}
await page.goto(base + best.href);
await settle();
await page.screenshot({ path: `${outDir}/ticket.png` });
console.log('ticket', best.href, best.len);
await browser.close();
