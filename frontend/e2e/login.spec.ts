import { expect, test } from '@playwright/test';
import { mockApi } from './support';

test.describe('sign-in', () => {
  test('shows the password form when anonymous', async ({ page }) => {
    await mockApi(page);
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
    await expect(page.getByLabel('Username or email')).toBeVisible();
    await expect(page.getByLabel('Password')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeDisabled();
  });

  test('rejects wrong credentials without revealing which one was wrong', async ({ page }) => {
    const state = await mockApi(page);
    await page.goto('/');
    await page.getByLabel('Username or email').fill('ada');
    await page.getByLabel('Password').fill('wrong');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByText('The username or password is incorrect.')).toBeVisible();
    await expect(page.getByLabel('Password')).toHaveValue('');
    expect(state.signedIn).toBe(false);
  });

  test('signs in and out', async ({ page }) => {
    const state = await mockApi(page);
    await page.goto('/');
    await page.getByLabel('Username or email').fill('ada');
    await page.getByLabel('Password').fill('correct horse battery');
    await page.getByRole('button', { name: 'Sign in' }).click();

    // Sign out lives in the account menu of the shell header.
    await page.locator('details:has(.turaco-signout) > summary').click();
    const signOut = page.getByRole('button', { name: 'Sign out' });
    await expect(signOut).toBeVisible();
    expect(state.signedIn).toBe(true);

    await signOut.click();
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
    expect(state.signedIn).toBe(false);
  });

  test('offers only the emergency form when it is the sole method', async ({ page }) => {
    await mockApi(page, { methods: { password: false, kerberos: false, emergency: true } });
    await page.goto('/');
    await expect(page.getByRole('main').getByRole('heading', { level: 1 })).not.toHaveText(
      'Sign in',
    );
    await expect(page.getByRole('alert').or(page.getByRole('status')).first()).toBeVisible();
  });
});

test('an anonymous deep link ends on the sign-in page', async ({ page }) => {
  await mockApi(page);
  await page.goto('/admin/roles');
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
});
