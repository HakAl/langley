import { test, expect } from '@playwright/test';
import { mockFlows, mockStats, setupMocks, authenticate } from './fixtures';

test.describe('Pagination Edge Cases', () => {
  test('empty results show empty state', async ({ page }) => {
    await setupMocks(page, { emptyFlows: true });
    await page.goto('/');
    await authenticate(page);

    await expect(page.getByText('No flows captured yet')).toBeVisible();
    await expect(page.locator('.flow-item')).toHaveCount(0);
  });

  test('single flow renders without count text', async ({ page }) => {
    const singleFlow = [mockFlows[0]];
    // Stats says 1 total — no "Showing X of Y" because filteredFlows.length === totalFlows
    await setupMocks(page, { flows: singleFlow });
    await page.route('**/api/stats**', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ ...mockStats, total_flows: 1 }),
      });
    });
    await page.goto('/');
    await authenticate(page);

    await expect(page.locator('.flow-item')).toHaveCount(1);
    await expect(page.locator('.flow-count')).not.toBeVisible();
  });

  test('shows count when filtered results fewer than total', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);

    // mockStats.total_flows is 150, mockFlows has 2 items
    // "Showing 2 of 150 flows" should be visible
    await expect(page.locator('.flow-count')).toContainText(/Showing 2 of/);
  });

  test('count text hides when stats total equals displayed count', async ({ page }) => {
    await setupMocks(page);
    // Override stats to match the 2 mock flows
    await page.route('**/api/stats**', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ ...mockStats, total_flows: 2 }),
      });
    });
    await page.goto('/');
    await authenticate(page);

    await expect(page.locator('.flow-item')).toHaveCount(2);
    await expect(page.locator('.flow-count')).not.toBeVisible();
  });

  test('filter narrowing to zero shows empty state', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);

    // Filter for a host that doesn't exist
    await page.getByPlaceholder('Filter by host...').fill('nonexistent.api.com');

    await expect(page.locator('.flow-item')).toHaveCount(0);
    await expect(page.getByText('No flows captured yet')).toBeVisible();
  });

  test('large result set renders correctly', async ({ page }) => {
    // Generate 50 flows (at the limit)
    const manyFlows = Array.from({ length: 50 }, (_, i) => ({
      id: `flow-${i}`,
      host: 'api.anthropic.com',
      method: 'POST',
      path: '/v1/messages',
      status_code: 200,
      status_text: 'OK',
      is_sse: false,
      timestamp: new Date(Date.now() - i * 1000).toISOString(),
      flow_integrity: 'complete',
    }));

    await setupMocks(page, { flows: manyFlows });
    await page.goto('/');
    await authenticate(page);

    await expect(page.locator('.flow-item')).toHaveCount(50);
  });

  test('keyboard nav wraps at boundaries', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);

    // With 2 flows, pressing k at the top should stay at 0
    const firstItem = page.locator('.flow-item').first();
    await expect(firstItem).toHaveClass(/keyboard-selected/);

    await page.keyboard.press('k'); // Already at top
    await expect(firstItem).toHaveClass(/keyboard-selected/);

    // Go to last item
    await page.keyboard.press('j');
    const lastItem = page.locator('.flow-item').nth(1);
    await expect(lastItem).toHaveClass(/keyboard-selected/);

    // Press j again at bottom — should stay at last
    await page.keyboard.press('j');
    await expect(lastItem).toHaveClass(/keyboard-selected/);
  });
});
