import { test, expect } from '@playwright/test';
import { mockFlowsExtended, setupMocks, authenticate, mockStats } from './fixtures';

test.describe('Combined Filters', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page, { flows: mockFlowsExtended });
    await page.goto('/');
    await authenticate(page);
  });

  test('host + status combined filters to matching flow', async ({ page }) => {
    // Filter by anthropic host + error status
    await page.getByPlaceholder('Filter by host...').fill('anthropic');
    await page.locator('select[aria-label="Filter by status"]').selectOption('error');

    // Only flow-ext-3 matches (anthropic + 500)
    await expect(page.locator('.flow-item')).toHaveCount(1);
    await expect(page.locator('.flow-item').first().locator('.status-code')).toContainText('500');
  });

  test('host + task combined filters to single result', async ({ page }) => {
    // Filter by cohere host + task-abc123
    await page.getByPlaceholder('Filter by host...').fill('cohere');
    await page.getByPlaceholder('Filter by task ID...').fill('task-abc123');

    // Only flow-ext-4 matches (cohere + task-abc123)
    await expect(page.locator('.flow-item')).toHaveCount(1);
    await expect(page.getByText('api.cohere.com')).toBeVisible();
  });

  test('all three filters active narrows to single result', async ({ page }) => {
    // Filter by anthropic + task-abc123 + success
    await page.getByPlaceholder('Filter by host...').fill('anthropic');
    await page.getByPlaceholder('Filter by task ID...').fill('task-abc123');
    await page.locator('select[aria-label="Filter by status"]').selectOption('success');

    // Only flow-ext-1 matches (anthropic + task-abc123 + 200)
    await expect(page.locator('.flow-item')).toHaveCount(1);
    await expect(page.locator('.flow-item').first().locator('.status-code')).toContainText('200');
  });

  test('clearing one filter expands results', async ({ page }) => {
    // Start with host + status
    await page.getByPlaceholder('Filter by host...').fill('anthropic');
    await page.locator('select[aria-label="Filter by status"]').selectOption('success');
    await expect(page.locator('.flow-item')).toHaveCount(1);

    // Clear status filter — all anthropic flows show (ext-1 and ext-3)
    await page.locator('select[aria-label="Filter by status"]').selectOption('all');
    await expect(page.locator('.flow-item')).toHaveCount(2);
  });

  test('count text shows "X of Y flows" when filtered', async ({ page }) => {
    // With 4 flows and totalFlows from stats (150), filtering should show count
    await page.getByPlaceholder('Filter by host...').fill('anthropic');

    // "Showing 2 of 150 flows" — totalFlows comes from stats
    await expect(page.locator('.flow-count')).toContainText(/Showing \d+ of/);
  });

  test('flow with no status_code passes through status filter', async ({ page }) => {
    // Add a flow with no status_code (in-progress)
    const flowsWithInProgress = [
      ...mockFlowsExtended,
      {
        id: 'flow-ext-inprogress',
        host: 'api.anthropic.com',
        method: 'POST',
        path: '/v1/messages',
        is_sse: false,
        timestamp: new Date().toISOString(),
        flow_integrity: 'incomplete',
        // no status_code
      },
    ];
    await setupMocks(page, { flows: flowsWithInProgress });
    await page.reload();

    // Filter by error — the in-progress flow should still be visible
    // because status filter short-circuits on falsy status_code (App.tsx:79-80)
    await page.locator('select[aria-label="Filter by status"]').selectOption('error');

    // The in-progress flow (no status_code) passes through,
    // plus flow-ext-2 (400) and flow-ext-3 (500)
    const items = page.locator('.flow-item');
    const count = await items.count();
    expect(count).toBeGreaterThanOrEqual(2);

    // Verify the in-progress flow is present (shows "...")
    await expect(page.locator('.flow-item .status-code').filter({ hasText: '...' })).toBeVisible();
  });
});

test.describe('Export Modal', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page, { flows: mockFlowsExtended });
    await page.goto('/');
    await authenticate(page);
  });

  test('selecting format opens modal with count', async ({ page }) => {
    // Mock the count endpoint
    await page.route('**/api/flows/count**', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ count: 42 }),
      });
    });

    // Select export format
    await page.locator('select[aria-label="Export flows"]').selectOption('json');

    // Modal should appear with count
    const modal = page.locator('.export-modal');
    await expect(modal).toBeVisible();
    await expect(modal).toContainText('42');
    await expect(modal).toContainText('JSON');
  });

  test('modal shows warning for >10k csv rows', async ({ page }) => {
    await page.route('**/api/flows/count**', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ count: 15000 }),
      });
    });

    await page.locator('select[aria-label="Export flows"]').selectOption('csv');

    const modal = page.locator('.export-modal');
    await expect(modal).toBeVisible();
    await expect(modal.locator('.warning')).toBeVisible();
    await expect(modal.locator('.warning')).toContainText('10,000');
  });

  test('cancel closes modal', async ({ page }) => {
    await page.route('**/api/flows/count**', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ count: 10 }),
      });
    });

    await page.locator('select[aria-label="Export flows"]').selectOption('json');
    await expect(page.locator('.export-modal')).toBeVisible();

    await page.getByRole('button', { name: 'Cancel' }).click();
    await expect(page.locator('.export-modal')).not.toBeVisible();
  });

  test('confirm triggers download request', async ({ page }) => {
    await page.route('**/api/flows/count**', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ count: 5 }),
      });
    });
    await page.route('**/api/flows/export**', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/octet-stream',
        headers: { 'Content-Disposition': 'attachment; filename="flows.json"' },
        body: '[]',
      });
    });

    await page.locator('select[aria-label="Export flows"]').selectOption('json');
    await expect(page.locator('.export-modal')).toBeVisible();

    const exportRequest = page.waitForRequest('**/api/flows/export**');
    await page.getByRole('button', { name: 'Download' }).click();

    const req = await exportRequest;
    expect(req.url()).toContain('format=json');
  });

  test('export includes active filter params', async ({ page }) => {
    await page.route('**/api/flows/count**', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ count: 2 }),
      });
    });
    await page.route('**/api/flows/export**', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/octet-stream',
        headers: { 'Content-Disposition': 'attachment; filename="flows.ndjson"' },
        body: '',
      });
    });

    // Set a host filter first
    await page.getByPlaceholder('Filter by host...').fill('anthropic');
    // Wait for the filter to take effect
    await expect(page.locator('.flow-item').first()).toContainText('anthropic');

    await page.locator('select[aria-label="Export flows"]').selectOption('ndjson');
    await expect(page.locator('.export-modal')).toBeVisible();

    const exportRequest = page.waitForRequest('**/api/flows/export**');
    await page.getByRole('button', { name: 'Download' }).click();

    const req = await exportRequest;
    expect(req.url()).toContain('host=anthropic');
    expect(req.url()).toContain('format=ndjson');
  });
});
