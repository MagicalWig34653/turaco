import { expect, test, type Page } from '@playwright/test';
import { mockApi } from './support';

async function signIn(page: Page) {
  await page.goto('/');
  await page.getByLabel('Username or email').fill('ada');
  await page.getByLabel('Password').fill('correct horse battery');
  await page.getByRole('button', { name: 'Sign in' }).click();
}

/** Location-scoped access (ADR-0040): the shell treats a permission held only at a Location as held anywhere. */
test.describe('Location-scoped access', () => {
  test('a holder of assets.view only at a Location reaches the Assets screen', async ({ page }) => {
    await mockApi(page, {
      modules: ['assets'],
      scopedPermissions: { 'assets.view': ['loc-site-nord'] },
    });
    await signIn(page);
    await page.goto('/assets');
    await expect(page.getByRole('main').getByRole('heading', { level: 1 })).toHaveText('Assets');
  });

  test('without any grant the Assets screen is refused', async ({ page }) => {
    await mockApi(page, { modules: ['assets'], permissions: [], scopedPermissions: {} });
    await signIn(page);
    await page.goto('/assets');
    await expect(page.getByRole('main').getByRole('heading', { level: 1 })).not.toHaveText(
      'Assets',
    );
  });

  test('a scoped holder of an unrelated permission does not gain Assets', async ({ page }) => {
    await mockApi(page, {
      modules: ['assets'],
      scopedPermissions: { 'tasks.work': ['loc-site-nord'] },
    });
    await signIn(page);
    await page.goto('/assets');
    await expect(page.getByRole('main').getByRole('heading', { level: 1 })).not.toHaveText(
      'Assets',
    );
  });
});
