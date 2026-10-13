// Captures the website/README screenshots from a running development stack seeded with the hospital
// simulation (docs/development/simulation-hospital.md): six key pages in each product theme.
// Usage (from the repository root, with API + Vite dev server running):
//   node tools/screenshots/capture.mjs [outDir]
// Output: <outDir>/<theme>/<page>.png for the themes turaco (light), dark and cyberpunk.
// Env: TURACO_URL (default http://localhost:5173), TURACO_USER, TURACO_PASSWORD.
import { mkdir } from 'node:fs/promises';
import { chromium } from '../../frontend/node_modules/playwright/index.mjs';

const base = process.env.TURACO_URL ?? 'http://localhost:5173';
const user = process.env.TURACO_USER ?? 'christian.hoffmann';
const password = process.env.TURACO_PASSWORD ?? 'turaco-sim-password';
const outDir = process.argv[2] ?? 'site/assets/screens';
const themes = ['turaco', 'dark', 'cyberpunk'];

const pages = [
  ['dashboard', '/'],
  ['service-desk', '/service-desk'],
  ['ticket', null], // the ticket with the most history, chosen once
  ['assets', '/assets'],
  ['my-work', '/my-work'],
  ['changes', '/changes'],
];

const browser = await chromium.launch();
let ticketHref = null;

for (const theme of themes) {
  await mkdir(`${outDir}/${theme}`, { recursive: true });
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    locale: 'en-US',
    colorScheme: theme === 'turaco' ? 'light' : 'dark',
    reducedMotion: 'reduce',
  });
  await ctx.addInitScript((t) => {
    localStorage.setItem('turaco.theme', t);
    localStorage.setItem('turaco.motion', 'reduced');
  }, theme);
  const page = await ctx.newPage();
  const settle = async () => {
    await page.waitForLoadState('networkidle');
    await page
      .waitForFunction(() => !document.querySelector('[class*="skeleton" i], [aria-busy="true"]'), null, { timeout: 15000 })
      .catch(() => {});
    await page.waitForTimeout(600);
  };

  await page.goto(base + '/');
  await page.locator('select').selectOption({ label: 'English' }).catch(() => {});
  const inputs = page.locator('input');
  await inputs.nth(0).fill(user);
  await inputs.nth(1).fill(password);
  await page.locator('button[type=submit]').click();
  await page.waitForLoadState('networkidle');
  await page.waitForTimeout(1500);

  if (!ticketHref) {
    await page.goto(base + '/service-desk?open=true&assignment=all');
    await settle();
    const hrefs = await page
      .locator('main a[href^="/support/"]')
      .evaluateAll((a) => a.map((x) => x.getAttribute('href')).filter((h) => h !== '/support/new').slice(0, 12));
    let best = 0;
    for (const href of hrefs) {
      await page.goto(base + href);
      await settle();
      const len = (await page.locator('main').innerText()).length;
      if (len > best) [best, ticketHref] = [len, href];
    }
  }

  for (const [name, path] of pages) {
    await page.goto(base + (path ?? ticketHref));
    await settle();
    await page.screenshot({ path: `${outDir}/${theme}/${name}.png` });
    console.log(theme, name, page.url());
  }
  await ctx.close();
}
await browser.close();
