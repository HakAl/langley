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

  test('settles at authoritative server total after page overlap and count refresh exhaustion', async ({ page }) => {
    let holdPage = false;
    let heldPage: (() => Promise<void>) | null = null;
    let serverTotal = 100;
    let countRequests = 0;
    const heldCounts: Array<() => Promise<void>> = [];

    await setupMocks(page);
    await page.route('**/api/flows/count*', async (route) => {
      countRequests++;
      const data = { count: countRequests === 1 ? 103 : serverTotal };
      if (countRequests <= 2) {
        heldCounts.push(() => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(data) }));
        return;
      }
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(data) });
    });
    await page.route('**/api/flows?*', async (route) => {
      const data = { flows: mockFlows, total: serverTotal };
      if (holdPage) {
        heldPage = () => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(data) });
        return;
      }
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(data) });
    });

    let wsServer: { send: (payload: string) => void } | null = null;
    await page.routeWebSocket('**/ws', (ws) => {
      wsServer = ws;
      ws.onMessage(() => {});
    });

    await page.goto('/');
    await authenticate(page);
    await expect(page.locator('.flow-count')).toContainText('100');

    const emitFlow = (id: string, statusCode: number | null) => {
      wsServer!.send(JSON.stringify({
        type: statusCode === null ? 'flow_start' : 'flow_complete',
        data: {
          id,
          host: 'api.anthropic.com',
          method: 'POST',
          path: `/v1/${id}`,
          is_sse: false,
          status_code: statusCode ?? undefined,
          timestamp: new Date().toISOString(),
          flow_integrity: 'complete',
        },
      }));
    };

    holdPage = true;
    await page.getByRole('button', { name: 'Next page' }).click();
    await expect.poll(() => heldPage !== null).toBe(true);

    emitFlow('new-flow', null);
    await expect(page.locator('.flow-count')).toContainText('101');

    serverTotal = 101;
    await heldPage!();
    await expect.poll(() => heldCounts.length).toBe(1);

    // Server already saved second-flow and third-flow (count = 103); notifications arrive across refreshes 1 and 2
    serverTotal = 103;
    emitFlow('second-flow', null);
    await heldCounts[0]();
    await expect.poll(() => heldCounts.length).toBe(2);

    emitFlow('third-flow', null);
    await heldCounts[1]();

    // Quiet recovery refresh (countRequests === 3) settles at 103 and stays 103 after completions
    await expect.poll(() => countRequests).toBe(3);
    emitFlow('new-flow', 200);
    emitFlow('second-flow', 200);
    emitFlow('third-flow', 200);
    await expect(page.locator('.flow-count')).toContainText('Showing 51–100 of 103 flows');
  });
});
