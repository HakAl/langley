import { test, expect } from '@playwright/test';
import { setupMocks, authenticate } from './fixtures';

test.describe('Mobile Viewport', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page);
  });

  test('nav visible with all buttons at 768px', async ({ page }) => {
    await page.setViewportSize({ width: 768, height: 1024 });
    await page.goto('/');
    await authenticate(page);

    await expect(page.getByRole('button', { name: 'Flows' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Analytics' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Tasks' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Tools' })).toBeVisible();
    await expect(page.getByRole('button', { name: /Anomalies/ })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Settings' })).toBeVisible();
  });

  test('all views render at 375px without error banner', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto('/');
    await authenticate(page);

    const views = ['Flows', 'Analytics', 'Tasks', 'Tools', 'Settings'];
    for (const viewName of views) {
      await page.getByRole('button', { name: viewName, exact: true }).click();
      // No error banner should appear
      await expect(page.locator('.error-banner')).not.toBeVisible();
    }

    // Check anomalies separately (regex match for badge)
    await page.getByRole('button', { name: /Anomalies/ }).click();
    await expect(page.locator('.error-banner')).not.toBeVisible();
  });
});
