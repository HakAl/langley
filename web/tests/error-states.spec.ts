import { test, expect } from '@playwright/test';
import { setupMocks, authenticate, mockStats, mockTasks } from './fixtures';

test.describe('Error States', () => {
  test('500 response shows error banner', async ({ page }) => {
    await page.route('**/api/flows?*', async (route) => {
      await route.fulfill({ status: 500, body: 'Internal Server Error' });
    });
    await page.route('**/api/stats**', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mockStats) });
    });
    await page.route('**/api/settings', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
    });

    await page.goto('/');
    await authenticate(page);

    const banner = page.locator('.error-banner[role="alert"]');
    await expect(banner).toBeVisible();
    await expect(banner).toContainText('Internal Server Error');
  });

  test('empty 500 body shows "HTTP 500" fallback', async ({ page }) => {
    await page.route('**/api/flows?*', async (route) => {
      await route.fulfill({ status: 500, body: '' });
    });
    await page.route('**/api/stats**', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mockStats) });
    });
    await page.route('**/api/settings', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
    });

    await page.goto('/');
    await authenticate(page);

    const banner = page.locator('.error-banner[role="alert"]');
    await expect(banner).toBeVisible();
    await expect(banner).toContainText('HTTP 500');
  });

  test('503 with body shows response text', async ({ page }) => {
    await page.route('**/api/flows?*', async (route) => {
      await route.fulfill({ status: 503, body: 'Service Unavailable' });
    });
    await page.route('**/api/stats**', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mockStats) });
    });
    await page.route('**/api/settings', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
    });

    await page.goto('/');
    await authenticate(page);

    const banner = page.locator('.error-banner[role="alert"]');
    await expect(banner).toBeVisible();
    await expect(banner).toContainText('Service Unavailable');
  });

  test('analytics view API failure shows error', async ({ page }) => {
    await setupMocks(page);
    // Override stats to fail
    await page.route('**/api/stats**', async (route) => {
      await route.fulfill({ status: 500, body: 'Stats unavailable' });
    });
    await page.goto('/');
    await authenticate(page);

    await page.getByRole('button', { name: 'Analytics' }).click();

    const banner = page.locator('.error-banner[role="alert"]');
    await expect(banner).toBeVisible();
    await expect(banner).toContainText('Stats unavailable');
  });

  test('tasks view API failure shows error', async ({ page }) => {
    await setupMocks(page);
    // Override tasks to fail
    await page.route('**/api/analytics/tasks**', async (route) => {
      await route.fulfill({ status: 500, body: 'Tasks unavailable' });
    });
    await page.goto('/');
    await authenticate(page);

    await page.getByRole('button', { name: 'Tasks' }).click();

    const banner = page.locator('.error-banner[role="alert"]');
    await expect(banner).toBeVisible();
    await expect(banner).toContainText('Tasks unavailable');
  });

  test('network failure shows error', async ({ page }) => {
    await page.route('**/api/flows?*', async (route) => {
      await route.abort('failed');
    });
    await page.route('**/api/stats**', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mockStats) });
    });
    await page.route('**/api/settings', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
    });

    await page.goto('/');
    await authenticate(page);

    const banner = page.locator('.error-banner[role="alert"]');
    await expect(banner).toBeVisible();
  });

  test('dismiss button removes error banner', async ({ page }) => {
    await page.route('**/api/flows?*', async (route) => {
      await route.fulfill({ status: 500, body: 'Server error' });
    });
    await page.route('**/api/stats**', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mockStats) });
    });
    await page.route('**/api/settings', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
    });

    await page.goto('/');
    await authenticate(page);

    const banner = page.locator('.error-banner[role="alert"]');
    await expect(banner).toBeVisible();

    await page.locator('[aria-label="Dismiss error"]').first().click();
    await expect(banner).not.toBeVisible();
  });
});
