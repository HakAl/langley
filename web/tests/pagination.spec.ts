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

  test('single flow shows count text', async ({ page }) => {
    const singleFlow = [mockFlows[0]];
    await setupMocks(page, { flows: singleFlow });
    await page.goto('/');
    await authenticate(page);

    await expect(page.locator('.flow-item')).toHaveCount(1);
    // totalFlows comes from API response (total: 1), count text shows "Showing 1–1 of 1 flows"
    await expect(page.locator('.flow-count')).toContainText('1');
  });

  test('shows count with range text', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);

    // mockFlows has 2 items, API returns total: 2
    // "Showing 1–2 of 2 flows"
    await expect(page.locator('.flow-count')).toContainText(/Showing 1/);
  });

  test('count text reflects total from API', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);

    await expect(page.locator('.flow-item')).toHaveCount(2);
    // totalFlows from API is 2 (matching the mock data count)
    await expect(page.locator('.flow-count')).toContainText('2');
  });

  test('filter narrowing to zero shows empty state', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);

    // Filter for a host that doesn't exist — server-side filtering returns empty
    await page.getByPlaceholder('Filter by host...').fill('nonexistent.api.com');

    await expect(page.locator('.flow-item')).toHaveCount(0);
    // Empty state text should appear
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
