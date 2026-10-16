import { test, expect } from '@playwright/test';
import { mockFlows, setupMocks, authenticate } from './fixtures';


test.describe('Flows View', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);
  });

  test('renders flow list', async ({ page }) => {
    await expect(page.getByText('api.anthropic.com')).toBeVisible();
    await expect(page.getByText('api.openai.com')).toBeVisible();
  });

  test('shows SSE badge for streaming flows', async ({ page }) => {
    await expect(page.locator('.badge.sse')).toBeVisible();
  });

  test('shows token counts and cost', async ({ page }) => {
    await expect(page.getByText('1,000')).toBeVisible(); // input tokens
    await expect(page.getByText('500')).toBeVisible(); // output tokens
    await expect(page.getByText('$0.0225')).toBeVisible(); // cost
  });

  test('opens flow detail on click', async ({ page }) => {
    await page.locator('.flow-item').first().click();
    await expect(page.locator('.flow-detail')).toBeVisible();
    await expect(page.getByText('POST /v1/messages')).toBeVisible();
  });

  test('closes flow detail on X button', async ({ page }) => {
    await page.locator('.flow-item').first().click();
    await expect(page.locator('.flow-detail')).toBeVisible();

    await page.locator('.close-btn').click();
    await expect(page.locator('.flow-detail')).not.toBeVisible();
  });

  test('filters by host', async ({ page }) => {
    await page.getByPlaceholder('Filter by host...').fill('anthropic');

    await expect(page.getByText('api.anthropic.com')).toBeVisible();
    await expect(page.getByText('api.openai.com')).not.toBeVisible();
  });

  test('filters by status', async ({ page }) => {
    await page.locator('select[aria-label="Filter by status"]').selectOption('error');

    // Only error flow (400) should be visible
    await expect(page.getByText('api.openai.com')).toBeVisible();
    await expect(page.getByText('api.anthropic.com')).not.toBeVisible();
  });

  test('shows empty state when no flows', async ({ page }) => {
    await setupMocks(page, { emptyFlows: true });
    await page.reload();

    await expect(page.getByText('No flows captured yet')).toBeVisible();
  });
});

test.describe('Analytics View', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);
    await page.getByRole('button', { name: 'Analytics' }).click();
  });

  test('renders stats cards', async ({ page }) => {
    await expect(page.getByText('Total Flows')).toBeVisible();
    await expect(page.getByText('150')).toBeVisible();

    await expect(page.getByText('Total Cost')).toBeVisible();
    await expect(page.getByText('$12.50')).toBeVisible();

    await expect(page.getByText('Total Tokens')).toBeVisible();
    await expect(page.getByText('75,000')).toBeVisible(); // 50000 + 25000

    await expect(page.locator('.stat-card').filter({ hasText: 'Tasks' })).toBeVisible();
    await expect(page.locator('.stat-card').filter({ hasText: '10' })).toBeVisible();
  });

  test('renders daily cost chart', async ({ page }) => {
    await expect(page.getByText('Daily Cost')).toBeVisible();
    await expect(page.locator('.bar-container')).toHaveCount(3);
  });
});

test.describe('Tasks View', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);
    await page.getByRole('button', { name: 'Tasks' }).click();
  });

  test('renders tasks table', async ({ page }) => {
    await expect(page.getByRole('columnheader', { name: 'Task ID' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Flows' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Cost' })).toBeVisible();
  });

  test('shows task data', async ({ page }) => {
    await expect(page.getByText('task-abc')).toBeVisible(); // truncated task ID
    await expect(page.getByRole('cell', { name: '5', exact: true })).toBeVisible(); // flow count
    await expect(page.getByRole('cell', { name: '$0.1500' })).toBeVisible(); // cost
  });

  test('clicking task navigates to flows with filter', async ({ page }) => {
    await page.locator('tbody tr').first().click();

    // Should switch to flows view with task filter applied
    await expect(page.getByRole('button', { name: 'Flows' })).toHaveClass(/active/);
    await expect(page.getByPlaceholder('Filter by task ID...')).toHaveValue('task-abc123');
  });
});

test.describe('Tools View', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);
    await page.getByRole('button', { name: 'Tools' }).click();
  });

  test('renders tools table', async ({ page }) => {
    await expect(page.getByRole('columnheader', { name: 'Tool' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Invocations' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Success Rate' })).toBeVisible();
  });

  test('shows tool stats', async ({ page }) => {
    await expect(page.getByRole('cell', { name: 'Read' })).toBeVisible();
    await expect(page.getByRole('cell', { name: '25', exact: true })).toBeVisible(); // invocations
    await expect(page.getByText('96.0%')).toBeVisible(); // success rate
  });

  test('color codes success rates', async ({ page }) => {
    // Read has 96% success - should be green
    const readRow = page.locator('tbody tr').filter({ hasText: 'Read' });
    await expect(readRow.locator('.success')).toBeVisible();

    // Edit has 80% success - should be warning/yellow
    const editRow = page.locator('tbody tr').filter({ hasText: 'Edit' });
    await expect(editRow.locator('.warning')).toBeVisible();
  });
});

test.describe('Anomalies View', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);
  });

  test('shows anomaly count badge in nav', async ({ page }) => {
    // Navigate to anomalies first to trigger the fetch
    await page.getByRole('button', { name: /Anomalies/ }).click();
    // Now the badge should appear
    await expect(page.locator('.nav .badge')).toHaveText('1');
  });

  test('renders anomaly list', async ({ page }) => {
    await page.getByRole('button', { name: /Anomalies/ }).click();

    await expect(page.getByText('high cost')).toBeVisible();
    await expect(page.getByText('Flow cost exceeded threshold')).toBeVisible();
    await expect(page.locator('.severity-badge.warning')).toBeVisible();
  });

  test('View Flow link navigates to flow detail', async ({ page }) => {
    await page.getByRole('button', { name: /Anomalies/ }).click();
    await page.getByRole('button', { name: 'View Flow' }).click();

    await expect(page.getByRole('button', { name: 'Flows' })).toHaveClass(/active/);
    await expect(page.locator('.flow-detail')).toBeVisible();
  });

  test('shows empty state when no anomalies', async ({ page }) => {
    await setupMocks(page, { emptyAnomalies: true });
    await page.reload();
    await page.getByRole('button', { name: 'Anomalies' }).click();

    await expect(page.getByText('No anomalies detected')).toBeVisible();
    await expect(page.getByText('Everything looks normal!')).toBeVisible();
  });
});

test.describe('Settings View', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);
    await page.getByRole('button', { name: 'Settings' }).click();
  });

  test('renders settings form', async ({ page }) => {
    await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible();
    await expect(page.getByLabel('Idle Gap (minutes)')).toBeVisible();
    await expect(page.getByLabel('Idle Gap (minutes)')).toHaveValue('5');
  });

  test('save button disabled when unchanged', async ({ page }) => {
    await expect(page.getByRole('button', { name: 'Save Settings' })).toBeDisabled();
  });

  test('save button enabled when value changed', async ({ page }) => {
    await page.getByLabel('Idle Gap (minutes)').fill('10');
    await expect(page.getByRole('button', { name: 'Save Settings' })).toBeEnabled();
  });

  test('reset button appears and works', async ({ page }) => {
    await page.getByLabel('Idle Gap (minutes)').fill('10');
    await expect(page.getByRole('button', { name: 'Reset' })).toBeVisible();

    await page.getByRole('button', { name: 'Reset' }).click();
    await expect(page.getByLabel('Idle Gap (minutes)')).toHaveValue('5');
  });

  test('saves settings on submit', async ({ page }) => {
    await page.getByLabel('Idle Gap (minutes)').fill('15');
    await page.getByRole('button', { name: 'Save Settings' }).click();

    // Button should be disabled after save (no changes)
    await expect(page.getByRole('button', { name: 'Save Settings' })).toBeDisabled();
    await expect(page.getByLabel('Idle Gap (minutes)')).toHaveValue('15');
  });
});

test.describe('Navigation', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);
  });

  test('all nav buttons are visible', async ({ page }) => {
    await expect(page.getByRole('button', { name: 'Flows' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Analytics' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Tasks' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Tools' })).toBeVisible();
    await expect(page.getByRole('button', { name: /Anomalies/ })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Settings' })).toBeVisible();
  });

  test('nav highlights active view', async ({ page }) => {
    await expect(page.getByRole('button', { name: 'Flows' })).toHaveClass(/active/);

    await page.getByRole('button', { name: 'Analytics' }).click();
    await expect(page.getByRole('button', { name: 'Analytics' })).toHaveClass(/active/);
    await expect(page.getByRole('button', { name: 'Flows' })).not.toHaveClass(/active/);
  });
});

test.describe('Connection Status', () => {
  test('shows disconnected initially', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByText('Disconnected')).toBeVisible();
  });
});

test.describe('Error Handling', () => {
  test('shows error banner on 401', async ({ page }) => {
    await page.routeWebSocket('**/ws', (ws) => { ws.onMessage(() => {}); });
    await page.route('**/api/flows?*', async (route) => {
      await route.fulfill({ status: 401 });
    });
    await page.route('**/api/stats**', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
    });
    await page.route('**/api/settings', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
    });

    await page.goto('/');
    await authenticate(page);

    const banner = page.locator('.error-banner[role="alert"]');
    await expect(banner).toBeVisible();
    await expect(banner).toContainText('HTTP 401');
  });
});

test.describe('Keyboard Shortcuts', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);
  });

  test('? toggles help modal', async ({ page }) => {
    // Help modal should not be visible initially
    await expect(page.getByRole('heading', { name: 'Keyboard Shortcuts' })).not.toBeVisible();

    // Press ? to open
    await page.keyboard.press('?');
    await expect(page.getByRole('heading', { name: 'Keyboard Shortcuts' })).toBeVisible();

    // Press ? again to close
    await page.keyboard.press('?');
    await expect(page.getByRole('heading', { name: 'Keyboard Shortcuts' })).not.toBeVisible();
  });

  test('help modal shows all shortcuts', async ({ page }) => {
    // Click body to ensure page has focus (not on an input)
    await page.locator('.flow-list').click();
    await page.keyboard.press('?');
    await expect(page.getByRole('heading', { name: 'Keyboard Shortcuts' })).toBeVisible();

    await expect(page.getByText('Navigate down / up')).toBeVisible();
    await expect(page.getByText('Select item')).toBeVisible();
    await expect(page.getByText('Focus search (flows view)')).toBeVisible();
    await expect(page.getByText('Close panel / blur input')).toBeVisible();
    await expect(page.getByText('Switch views')).toBeVisible();
    await expect(page.getByText('Toggle this help')).toBeVisible();
  });

  test('Escape closes help modal', async ({ page }) => {
    await page.locator('.flow-list').click();
    await page.keyboard.press('?');
    await expect(page.getByRole('heading', { name: 'Keyboard Shortcuts' })).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(page.getByRole('heading', { name: 'Keyboard Shortcuts' })).not.toBeVisible();
  });

  test('Escape closes flow detail panel', async ({ page }) => {
    // Open flow detail
    await page.locator('.flow-item').first().click();
    await expect(page.locator('.flow-detail')).toBeVisible();

    // Press Escape to close
    await page.keyboard.press('Escape');
    await expect(page.locator('.flow-detail')).not.toBeVisible();
  });

  test('number keys switch views', async ({ page }) => {
    // Start on Flows (1)
    await expect(page.getByRole('button', { name: 'Flows' })).toHaveClass(/active/);

    // Press 2 for Analytics
    await page.keyboard.press('2');
    await expect(page.getByRole('button', { name: 'Analytics' })).toHaveClass(/active/);
    await expect(page.getByText('Total Flows')).toBeVisible();

    // Press 3 for Tasks
    await page.keyboard.press('3');
    await expect(page.getByRole('button', { name: 'Tasks' })).toHaveClass(/active/);
    await expect(page.getByRole('columnheader', { name: 'Task ID' })).toBeVisible();

    // Press 4 for Tools
    await page.keyboard.press('4');
    await expect(page.getByRole('button', { name: 'Tools' })).toHaveClass(/active/);
    await expect(page.getByRole('columnheader', { name: 'Tool' })).toBeVisible();

    // Press 5 for Anomalies
    await page.keyboard.press('5');
    await expect(page.getByRole('button', { name: /Anomalies/ })).toHaveClass(/active/);

    // Press 6 for Settings
    await page.keyboard.press('6');
    await expect(page.getByRole('button', { name: 'Settings', exact: true })).toHaveClass(/active/);
    await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible();

    // Press 1 to go back to Flows
    await page.keyboard.press('1');
    await expect(page.getByRole('button', { name: 'Flows' })).toHaveClass(/active/);
  });

  test('/ focuses search input in flows view', async ({ page }) => {
    const searchInput = page.getByPlaceholder('Filter by host...');
    await expect(searchInput).not.toBeFocused();

    await page.keyboard.press('/');
    await expect(searchInput).toBeFocused();
  });

  test('/ does not focus search in other views', async ({ page }) => {
    // Click to ensure focus, then switch to Tasks view
    await page.locator('.flow-list').click();
    await page.keyboard.press('3');
    await expect(page.getByRole('button', { name: 'Tasks' })).toHaveClass(/active/);
    await expect(page.getByRole('columnheader', { name: 'Task ID' })).toBeVisible();

    // Press / should not focus search (not visible in Tasks view)
    await page.keyboard.press('/');

    // Search input is not visible in Tasks view, so it shouldn't be focused
    const searchInput = page.getByPlaceholder('Filter by host...');
    await expect(searchInput).not.toBeVisible();
  });

  test('j/k navigate flow list', async ({ page }) => {
    // First item should be keyboard-selected initially
    const firstItem = page.locator('.flow-item').first();
    const secondItem = page.locator('.flow-item').nth(1);

    await expect(firstItem).toHaveClass(/keyboard-selected/);
    await expect(secondItem).not.toHaveClass(/keyboard-selected/);

    // Press j to move down
    await page.keyboard.press('j');
    await expect(firstItem).not.toHaveClass(/keyboard-selected/);
    await expect(secondItem).toHaveClass(/keyboard-selected/);

    // Press k to move back up
    await page.keyboard.press('k');
    await expect(firstItem).toHaveClass(/keyboard-selected/);
    await expect(secondItem).not.toHaveClass(/keyboard-selected/);
  });

  test('Enter selects keyboard-selected flow', async ({ page }) => {
    // Flow detail should not be visible
    await expect(page.locator('.flow-detail')).not.toBeVisible();

    // Click to ensure page has focus (not on an input)
    await page.locator('.flow-list').click();

    // Press Enter to select first flow
    await page.keyboard.press('Enter');
    await expect(page.locator('.flow-detail')).toBeVisible();
  });

  test('j/k navigate tasks list', async ({ page }) => {
    // Switch to Tasks view via click (to ensure focus)
    await page.getByRole('button', { name: 'Tasks' }).click();

    // Wait for task data to load (task-abc is the truncated task ID)
    await expect(page.getByText('task-abc')).toBeVisible();

    // First task row should be keyboard-selected by default
    const firstRow = page.locator('tbody tr').first();
    await expect(firstRow).toHaveClass(/keyboard-selected/);
  });

  test('Enter on task navigates to flows with filter', async ({ page }) => {
    // Switch to Tasks view via click
    await page.getByRole('button', { name: 'Tasks' }).click();
    await expect(page.getByRole('columnheader', { name: 'Task ID' })).toBeVisible();

    // Click on table to ensure keyboard focus, then press Enter
    await page.locator('tbody').click();
    await page.keyboard.press('Enter');

    // Should switch to Flows view with task filter
    await expect(page.getByRole('button', { name: 'Flows' })).toHaveClass(/active/);
    await expect(page.getByPlaceholder('Filter by task ID...')).toHaveValue('task-abc123');
  });

  test('keyboard shortcuts ignored when typing in input', async ({ page }) => {
    // Focus the search input
    await page.getByPlaceholder('Filter by host...').focus();

    // Type 'j' - should go into input, not navigate
    await page.keyboard.type('j');
    await expect(page.getByPlaceholder('Filter by host...')).toHaveValue('j');

    // View should not have changed
    await expect(page.getByRole('button', { name: 'Flows' })).toHaveClass(/active/);
  });

  test('Escape blurs focused input', async ({ page }) => {
    const searchInput = page.getByPlaceholder('Filter by host...');

    // Focus and type
    await searchInput.focus();
    await expect(searchInput).toBeFocused();

    // Press Escape to blur
    await page.keyboard.press('Escape');
    await expect(searchInput).not.toBeFocused();
  });

  test('flow list has ARIA listbox attributes', async ({ page }) => {
    const flowList = page.locator('.flow-list');
    await expect(flowList).toHaveAttribute('role', 'listbox');
    await expect(flowList).toHaveAttribute('aria-label', 'Flow list');

    const firstItem = page.locator('.flow-item').first();
    await expect(firstItem).toHaveAttribute('role', 'option');
    await expect(firstItem).toHaveAttribute('aria-selected', 'true');
  });
});

test.describe('Responsive Detail Panel (langley-gzom)', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);
  });

  test('detail panel shows as overlay at narrow widths', async ({ page }) => {
    await page.setViewportSize({ width: 800, height: 600 });

    await page.locator('.flow-item').first().click();
    await expect(page.locator('.flow-detail')).toBeVisible();
    await expect(page.locator('.detail-overlay')).toBeVisible();
  });

  test('detail panel is accessible at mobile width', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });

    await page.locator('.flow-item').first().click();
    await expect(page.locator('.flow-detail')).toBeVisible();
    await expect(page.getByText('POST /v1/messages')).toBeVisible();
  });

  test('overlay backdrop closes detail panel', async ({ page }) => {
    await page.setViewportSize({ width: 800, height: 600 });

    await page.locator('.flow-item').first().click();
    await expect(page.locator('.flow-detail')).toBeVisible();

    // Click the overlay backdrop (not the panel itself)
    await page.locator('.detail-overlay').click({ position: { x: 10, y: 300 } });
    await expect(page.locator('.flow-detail')).not.toBeVisible();
  });

  test('Escape closes overlay detail panel', async ({ page }) => {
    await page.setViewportSize({ width: 800, height: 600 });

    await page.locator('.flow-item').first().click();
    await expect(page.locator('.flow-detail')).toBeVisible();

    await page.keyboard.press('Escape');
    await expect(page.locator('.flow-detail')).not.toBeVisible();
  });

  test('detail panel has no overlay at wide widths', async ({ page }) => {
    await page.setViewportSize({ width: 1400, height: 900 });

    await page.locator('.flow-item').first().click();
    await expect(page.locator('.flow-detail')).toBeVisible();

    // Overlay should exist in DOM but not be visible (no fixed positioning at wide widths)
    const overlay = page.locator('.detail-overlay');
    await expect(overlay).toBeAttached();
    await expect(overlay).not.toHaveCSS('position', 'fixed');
  });
});

test.describe('Hash Routing', () => {
  // Deep linking tests (langley-262)
  test('/#analytics loads analytics view on page load', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/#analytics');
    await authenticate(page);

    await expect(page.getByRole('button', { name: 'Analytics' })).toHaveClass(/active/);
    await expect(page.getByText('Total Flows')).toBeVisible();
  });

  test('/#settings loads settings view on page load', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/#settings');
    await authenticate(page);

    await expect(page.getByRole('button', { name: 'Settings', exact: true })).toHaveClass(/active/);
    await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible();
  });

  test('/#garbage falls back to flows', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/#garbage');
    await authenticate(page);

    await expect(page.getByRole('button', { name: 'Flows' })).toHaveClass(/active/);
  });

  test('/# (empty hash) loads flows', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/#');
    await authenticate(page);

    await expect(page.getByRole('button', { name: 'Flows' })).toHaveClass(/active/);
  });

  // Back button tests (langley-8rh)
  test('back button returns to previous view after navigation', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);

    // Navigate: flows -> analytics -> tasks
    await page.getByRole('button', { name: 'Analytics' }).click();
    await expect(page.getByRole('button', { name: 'Analytics' })).toHaveClass(/active/);

    await page.getByRole('button', { name: 'Tasks' }).click();
    await expect(page.getByRole('button', { name: 'Tasks' })).toHaveClass(/active/);

    // Go back to analytics
    await page.goBack();
    await expect(page.getByRole('button', { name: 'Analytics' })).toHaveClass(/active/);

    // Go back to flows
    await page.goBack();
    await expect(page.getByRole('button', { name: 'Flows' })).toHaveClass(/active/);
  });

  test('clicking nav updates URL hash', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);

    await page.getByRole('button', { name: 'Analytics' }).click();
    await expect(page).toHaveURL(/#analytics/);

    await page.getByRole('button', { name: 'Tools' }).click();
    await expect(page).toHaveURL(/#tools/);
  });

  test('keyboard shortcut updates URL hash', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);

    // Click body to ensure keyboard focus after reload
    await page.locator('.flow-list').click();

    await page.keyboard.press('2');
    await expect(page).toHaveURL(/#analytics/);

    await page.keyboard.press('6');
    await expect(page).toHaveURL(/#settings/);
  });
});

test.describe('Flow Inspector & Time Range UX (langley-63xq)', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page);
    await page.route('**/api/flows/flow-1', route => route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        ...mockFlows[0],
        request_headers: { 'x-request-test': ['request header value'] },
        response_headers: { 'x-response-test': ['response header value'] },
        request_body: JSON.stringify({
          model: 'claude-sonnet-4-20250514',
          system: 'Helpful assistant',
          messages: [{ role: 'user', content: 'Find the answer' }],
          tools: [{ name: 'search' }],
        }),
        response_body: 'event: content_block_delta\ndata: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Reconstructed answer"}}\n\n',
      }),
    }));
    await page.goto('/');
    await authenticate(page);
  });

  test('live intervals select exact windows and readable analytics labels', async ({ page }) => {
    const range = page.getByLabel('Time range');
    await expect(range).toHaveValue('24h');
    for (const [key, minutes] of [['5m', 5], ['15m', 15], ['1h', 60], ['6h', 360]] as const) {
      const request = page.waitForRequest(request => {
        const url = new URL(request.url());
        return url.pathname === '/api/flows' && url.searchParams.has('start_time') &&
          Math.abs(Date.parse(url.searchParams.get('end_time')!) - Date.parse(url.searchParams.get('start_time')!) - minutes * 60000) < 2;
      });
      await range.selectOption(key);
      await request;
      await expect(range).toHaveValue(key);
    }
    await page.getByRole('button', { name: 'Analytics', exact: true }).click();
    await page.getByLabel('Time range').selectOption('5m');
    await expect(page.getByText('Cost over last 5 minutes')).toBeVisible();
    await page.getByLabel('Time range').selectOption('all');
    await expect(page.getByText('Cost over all time')).toBeVisible();
  });

  test('formatted conversation is immediately visible and raw bodies and headers remain inspectable', async ({ page }) => {
    await expect(page.locator('.flow-item').first().locator('.flow-model')).toHaveText(mockFlows[0].model);
    await expect(page.locator('.flow-item').first().locator('.flow-duration')).toHaveText('1.50s');
    await page.locator('.flow-item').first().click();
    await expect(page.getByRole('tab', { name: 'Formatted', exact: true })).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByText('Helpful assistant')).toBeVisible();
    await expect(page.getByText('Reconstructed answer')).toBeVisible();
    await expect(page.locator('.llm-tools-summary')).toContainText('1 tool:');
    await expect(page.locator('.llm-tools-summary')).toContainText('search');
    await expect(page.getByRole('button', { name: /Show all/ })).toHaveCount(0);
    await page.getByRole('tab', { name: 'Request', exact: true }).click();
    await expect(page.locator('.body-content pre')).toContainText('Find the answer');
    await page.getByRole('tab', { name: 'Response', exact: true }).click();
    await expect(page.locator('.body-content pre')).toContainText('event: content_block_delta');
    await page.getByRole('tab', { name: 'Headers', exact: true }).click();
    await expect(page.locator('.body-panel')).toContainText('x-request-test:');
    await expect(page.locator('.body-panel')).toContainText('request header value');
    await expect(page.locator('.body-panel')).toContainText('x-response-test:');
    await expect(page.locator('.body-panel')).toContainText('response header value');
  });

  test('desktop inspector occupies two thirds and has an internal full-height scroll region', async ({ page }) => {
    await page.setViewportSize({ width: 1400, height: 900 });
    await page.locator('.flow-item').first().click();
    const detail = await page.locator('.flow-detail').boundingBox();
    const list = await page.locator('.content-area').boundingBox();
    expect(detail!.width / list!.width).toBeGreaterThan(1.8);
    expect(detail!.width / list!.width).toBeLessThan(2.2);
    const scroll = await page.locator('.body-panel').evaluate(element => ({
      overflow: getComputedStyle(element).overflowY,
      height: element.clientHeight,
    }));
    expect(scroll.overflow).toBe('auto');
    expect(scroll.height).toBeGreaterThan(300);
    await expect(page.locator('.detail-overlay')).not.toBeVisible();
  });

  test('long raw bodies remain fully available in the scrolling inspector', async ({ page }) => {
    const lines = Array.from({ length: 100 }, (_, index) => `line ${index}`).join('\n');
    await page.route('**/api/flows/flow-1', route => route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ ...mockFlows[0], request_body: '', response_body: lines }),
    }));
    await page.locator('.flow-item').first().click();
    await expect(page.getByRole('tab', { name: 'Response', exact: true })).toHaveAttribute('aria-selected', 'true');
    await expect(page.locator('.body-content pre')).toContainText('line 99');
    const dimensions = await page.locator('.body-panel').evaluate(element => ({
      content: element.scrollHeight,
      viewport: element.clientHeight,
    }));
    expect(dimensions.content).toBeGreaterThan(dimensions.viewport);
    await expect(page.getByRole('button', { name: /Show all/ })).toHaveCount(0);
  });

  test('preserves selected body tab across flow selections and toggles <summary> with Enter', async ({ page }) => {
    await page.route('**/api/flows/flow-1', route => route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        ...mockFlows[0],
        request_body: JSON.stringify({
          model: 'claude-sonnet-4-20250514',
          system: 'Long system prompt. '.repeat(45),
          messages: [{ role: 'user', content: 'First flow question' }],
        }),
        response_body: 'event: message_start\ndata: {"type":"message_start","message":{"model":"claude-sonnet-4-20250514"}}\n\nevent: content_block_delta\ndata: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Partial answer"}}\n\nevent: error\ndata: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}\n\n',
      }),
    }));
    await page.route('**/api/flows/flow-2', route => route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        ...mockFlows[1],
        request_body: JSON.stringify({
          model: 'gpt-4o',
          messages: [{ role: 'user', content: [{ type: { toString: null } }, { type: 'text', text: 'Second flow question' }] }],
        }),
        response_body: JSON.stringify({
          model: 'gpt-4o',
          choices: [{ index: 0, finish_reason: 'stop', message: { role: 'assistant', content: 'Second flow answer' } }],
        }),
      }),
    }));

    await page.locator('.flow-item').first().click();
    await expect(page.locator('.detail-alert.error')).toContainText('Stream error: overloaded_error: Overloaded');

    // Long system prompt starts collapsed; pressing Enter on <summary> opens it
    const systemDetails = page.locator('details.llm-system');
    await expect(systemDetails).not.toHaveJSProperty('open', true);
    const summary = systemDetails.locator('summary');
    await summary.focus();
    await page.keyboard.press('Enter');
    await expect(systemDetails).toHaveJSProperty('open', true);

    // Switch to Response tab, then select flow-2; Response tab remains selected
    await page.getByRole('tab', { name: 'Response', exact: true }).click();
    await expect(page.getByRole('tab', { name: 'Response', exact: true })).toHaveAttribute('aria-selected', 'true');
    await page.locator('.flow-item').nth(1).click();
    await expect(page.getByRole('tab', { name: 'Response', exact: true })).toHaveAttribute('aria-selected', 'true');
    await expect(page.locator('.body-content pre')).toContainText('Second flow answer');

    // Malformed discriminator object in flow-2 does not crash Formatted or Request views
    await page.getByRole('tab', { name: 'Formatted', exact: true }).click();
    await expect(page.getByText('Second flow question')).toBeVisible();
    await expect(page.getByText('[unknown]')).toBeVisible();
  });

  test('live time window expires aged-out rows and rejects out-of-window WebSocket completions', async ({ page }) => {
    const now = Date.now();
    await page.clock.install({ time: new Date(now) });
    await page.addInitScript(() => {
      (window as unknown as { __sockets: unknown[] }).__sockets = [];
      class FakeSocket {
        static OPEN = 1;
        readyState = 1;
        onopen?: (ev: unknown) => void;
        onmessage?: (ev: { data: string }) => void;
        constructor() {
          (window as unknown as { __sockets: FakeSocket[] }).__sockets.push(this);
          setTimeout(() => this.onopen?.({}), 0);
        }
        close() { this.readyState = 3; }
        send() {}
      }
      (window as unknown as { WebSocket: unknown }).WebSocket = FakeSocket;
    });

    const recentFlow = {
      ...mockFlows[0],
      id: 'flow-recent',
      timestamp: new Date(now - 270_000).toISOString(), // 4.5m old
    };
    const oldFlow = {
      ...mockFlows[1],
      id: 'flow-old',
      timestamp: new Date(now - 600_000).toISOString(), // 10m old
    };

    await page.route('**/api/flows?*', route => {
      const url = new URL(route.request().url());
      const start = Date.parse(url.searchParams.get('start_time') ?? '1970-01-01T00:00:00Z');
      const filtered = [recentFlow, oldFlow].filter(f => Date.parse(f.timestamp) >= start);
      return route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ flows: filtered, total: filtered.length, limit: 50, offset: 0 }),
      });
    });

    await page.goto('/');
    await authenticate(page);
    await page.getByLabel('Time range').selectOption('5m');
    await expect(page.locator('.flow-item')).toHaveCount(1);

    // Advance clock by 60s so recentFlow becomes 5.5m old and expires from the 5m window
    await page.clock.fastForward(60_000);
    await expect(page.locator('.flow-item')).toHaveCount(0);

    // Emit a WebSocket flow_complete for the 10m-old flow; it must be ignored under the 5m filter
    await page.evaluate(f => {
      const sockets = (window as unknown as { __sockets: Array<{ readyState: number; onmessage?: (ev: { data: string }) => void }> }).__sockets;
      sockets.filter(s => s.readyState === 1).forEach(s => s.onmessage?.({ data: JSON.stringify({ type: 'flow_complete', data: f }) }));
    }, oldFlow);
    await expect(page.locator('.flow-item')).toHaveCount(0);
    await expect(page.locator('.flow-count')).toHaveText('Showing 0 of 0 flows');
  });

  test('paginated live window reconciles off-page expiry and clamps when the last page expires', async ({ page }) => {
    const now = Date.now();
    await page.clock.install({ time: new Date(now) });
    await page.addInitScript(() => {
      class FakeSocket {
        static OPEN = 1;
        readyState = 1;
        onopen?: (ev: unknown) => void;
        constructor() { setTimeout(() => this.onopen?.({}), 0); }
        close() { this.readyState = 3; }
        send() {}
      }
      (window as unknown as { WebSocket: unknown }).WebSocket = FakeSocket;
    });

    const dataset = Array.from({ length: 100 }, (_, i) => ({
      ...mockFlows[0],
      id: `f-${i}`,
      timestamp: new Date(now - (i < 50 ? 120_000 : 240_000)).toISOString(),
    }));

    await page.route('**/api/flows?*', route => {
      const url = new URL(route.request().url());
      const start = Date.parse(url.searchParams.get('start_time') ?? '1970-01-01T00:00:00Z');
      const filtered = dataset.filter(f => Date.parse(f.timestamp) >= start);
      const offset = Number(url.searchParams.get('offset') ?? 0);
      return route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ flows: filtered.slice(offset, offset + 50), total: filtered.length, limit: 50, offset }),
      });
    });

    await page.goto('/');
    await authenticate(page);
    await page.getByLabel('Time range').selectOption('5m');
    await expect(page.locator('.flow-item')).toHaveCount(50);
    await expect(page.locator('.flow-count')).toHaveText('Showing 1–50 of 100 flows');

    // Navigate to page 2 (50 older flows, 4m old), then advance 61s so page 2 expires and clamps back to page 1
    await page.getByRole('button', { name: 'Next page' }).click();
    await expect(page.locator('.flow-count')).toHaveText('Showing 51–100 of 100 flows');
    await page.clock.fastForward(61_000);
    await expect(page.locator('.flow-count')).toHaveText('Showing 1–50 of 50 flows');
    await expect(page.locator('.flow-item')).toHaveCount(50);

    // Advance another 120s so the remaining 50 flows expire
    await page.clock.fastForward(120_000);
    await expect(page.locator('.flow-item')).toHaveCount(0);
    await expect(page.locator('.flow-count')).toHaveText('Showing 0 of 0 flows');
  });

  test('middle-page view reconciles oldest off-page cohort expiry and clamps when middle cohort expires', async ({ page }) => {
    const now = Date.now();
    await page.clock.install({ time: new Date(now) });
    await page.addInitScript(() => {
      class FakeSocket {
        static OPEN = 1;
        readyState = 1;
        onopen?: (ev: unknown) => void;
        constructor() { setTimeout(() => this.onopen?.({}), 0); }
        close() { this.readyState = 3; }
        send() {}
      }
      (window as unknown as { WebSocket: unknown }).WebSocket = FakeSocket;
    });

    const dataset = Array.from({ length: 150 }, (_, i) => ({
      ...mockFlows[0],
      id: `f-${i}`,
      timestamp: new Date(now - (i < 50 ? 120_000 : i < 100 ? 180_000 : 240_000)).toISOString(),
    }));

    await page.route('**/api/flows?*', route => {
      const url = new URL(route.request().url());
      const start = Date.parse(url.searchParams.get('start_time') ?? '1970-01-01T00:00:00Z');
      const filtered = dataset.filter(f => Date.parse(f.timestamp) >= start);
      const offset = Number(url.searchParams.get('offset') ?? 0);
      return route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ flows: filtered.slice(offset, offset + 50), total: filtered.length, limit: 50, offset }),
      });
    });

    await page.goto('/');
    await authenticate(page);
    await page.getByLabel('Time range').selectOption('5m');
    await page.getByRole('button', { name: 'Page 2' }).click();
    await expect(page.locator('.flow-item')).toHaveCount(50);
    await expect(page.locator('.flow-count')).toHaveText('Showing 51–100 of 150 flows');

    // +61s: oldest cohort (page 3, 4m old) expires off-page
    await page.clock.fastForward(61_000);
    await expect(page.locator('.flow-count')).toHaveText('Showing 51–100 of 100 flows');
    await expect(page.locator('.flow-item')).toHaveCount(50);

    // +60s: middle cohort (page 2, 3m old) expires and clamps to page 1
    await page.clock.fastForward(60_000);
    await expect(page.locator('.flow-count')).toHaveText('Showing 1–50 of 50 flows');
    await expect(page.locator('.flow-item')).toHaveCount(50);
  });

  test('background off-page refresh recovers after transient HTTP 503 and preserves local expiry', async ({ page }) => {
    const now = Date.now();
    await page.clock.install({ time: new Date(now) });
    await page.addInitScript(() => {
      class FakeSocket {
        static OPEN = 1;
        readyState = 1;
        onopen?: (ev: unknown) => void;
        constructor() { setTimeout(() => this.onopen?.({}), 0); }
        close() { this.readyState = 3; }
        send() {}
      }
      (window as unknown as { WebSocket: unknown }).WebSocket = FakeSocket;
    });

    const dataset = Array.from({ length: 100 }, (_, i) => ({
      ...mockFlows[0],
      id: `f-${i}`,
      timestamp: new Date(now - (i < 50 ? 120_000 : 240_000)).toISOString(),
    }));

    let fiveMinFetches = 0;
    await page.route('**/api/flows?*', route => {
      const url = new URL(route.request().url());
      const start = Date.parse(url.searchParams.get('start_time') ?? '1970-01-01T00:00:00Z');
      const end = Date.parse(url.searchParams.get('end_time') ?? '1970-01-01T00:00:00Z');
      const isFiveMinWindow = Math.abs((end - start) - 300_000) < 5_000;
      if (isFiveMinWindow) {
        fiveMinFetches += 1;
        // Fail the first background poll (second 5m fetch) with HTTP 503
        if (fiveMinFetches === 2) {
          return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'busy' }) });
        }
      }
      const filtered = dataset.filter(f => Date.parse(f.timestamp) >= start);
      const offset = Number(url.searchParams.get('offset') ?? 0);
      return route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ flows: filtered.slice(offset, offset + 50), total: filtered.length, limit: 50, offset }),
      });
    });

    await page.goto('/');
    await authenticate(page);
    await page.getByLabel('Time range').selectOption('5m');
    await expect(page.locator('.flow-count')).toHaveText('Showing 1–50 of 100 flows');

    await page.clock.fastForward(61_000);
    await expect(page.locator('.flow-count')).toHaveText('Showing 1–50 of 50 flows');
    await expect(page.locator('.flow-item')).toHaveCount(50);

    await page.clock.fastForward(120_000);
    await expect(page.locator('.flow-item')).toHaveCount(0);
    await expect(page.locator('.flow-count')).toHaveText('Showing 0 of 0 flows');
  });

  test('clicking active page button does not disable expiry and WebSocket pruning clamps empty nonzero page', async ({ page }) => {
    const now = Date.now();
    await page.clock.install({ time: new Date(now) });
    await page.addInitScript(() => {
      const sockets: unknown[] = [];
      (window as unknown as { __sockets: unknown[] }).__sockets = sockets;
      class FakeSocket {
        static OPEN = 1;
        readyState = 1;
        onopen?: (ev: unknown) => void;
        onmessage?: (ev: { data: string }) => void;
        constructor() {
          sockets.push(this);
          setTimeout(() => this.onopen?.({}), 0);
        }
        close() { this.readyState = 3; }
        send() {}
      }
      (window as unknown as { WebSocket: unknown }).WebSocket = FakeSocket;
    });

    const dataset = Array.from({ length: 51 }, (_, i) => ({
      ...mockFlows[0],
      id: `f-${i}`,
      timestamp: new Date(now - (i < 50 ? 120_000 : 240_000)).toISOString(),
    }));
    const oldFlow = {
      ...mockFlows[1],
      id: 'flow-ancient',
      timestamp: new Date(now - 600_000).toISOString(),
    };

    await page.route('**/api/flows?*', route => {
      const url = new URL(route.request().url());
      const start = Date.parse(url.searchParams.get('start_time') ?? '1970-01-01T00:00:00Z');
      const filtered = dataset.filter(f => Date.parse(f.timestamp) >= start);
      const offset = Number(url.searchParams.get('offset') ?? 0);
      return route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ flows: filtered.slice(offset, offset + 50), total: filtered.length, limit: 50, offset }),
      });
    });

    await page.goto('/');
    await authenticate(page);
    await page.getByLabel('Time range').selectOption('5m');
    await page.getByRole('button', { name: 'Page 2' }).click();
    await expect(page.locator('.flow-count')).toHaveText('Showing 51–51 of 51 flows');

    // Click already-selected Page 2 button
    await page.getByRole('button', { name: 'Page 2' }).click();

    // Advance to boundary + 1ms (60_001ms), before the +10ms local timer fires, and deliver a WS event
    await page.clock.runFor(60_001);
    await page.evaluate(f => {
      const sockets = (window as unknown as { __sockets: Array<{ readyState: number; onmessage?: (ev: { data: string }) => void }> }).__sockets;
      sockets.filter(s => s.readyState === 1).forEach(s => s.onmessage?.({ data: JSON.stringify({ type: 'flow_complete', data: f }) }));
    }, oldFlow);

    await expect(page.locator('.flow-count')).toHaveText('Showing 1–50 of 50 flows');
    await expect(page.locator('.flow-item')).toHaveCount(50);
  });

  test('reconnect superseding a held host-filter request settles loading and preserves expiry', async ({ page }) => {
    const now = Date.now();
    await page.clock.install({ time: new Date(now) });
    await page.addInitScript(() => {
      const sockets: unknown[] = [];
      (window as unknown as { __sockets: unknown[] }).__sockets = sockets;
      class FakeSocket {
        static OPEN = 1;
        readyState = 1;
        onopen?: (ev: unknown) => void;
        onclose?: (ev: unknown) => void;
        constructor() {
          sockets.push(this);
          setTimeout(() => this.onopen?.({}), 0);
        }
        close() {
          this.readyState = 3;
          this.onclose?.({});
        }
        send() {}
      }
      (window as unknown as { WebSocket: unknown }).WebSocket = FakeSocket;
    });

    const singleFlow = {
      ...mockFlows[0],
      id: 'f-0',
      host: 'api.example.test',
      timestamp: new Date(now - 240_000).toISOString(),
    };

    let releaseHeldHostFetch: (() => void) | null = null;
    let hostFetchCount = 0;

    await page.route('**/api/flows?*', async route => {
      const url = new URL(route.request().url());
      const start = Date.parse(url.searchParams.get('start_time') ?? '1970-01-01T00:00:00Z');
      const host = url.searchParams.get('host');
      const filtered = [singleFlow].filter(f => Date.parse(f.timestamp) >= start && (!host || f.host === host));
      if (host === 'api.example.test') {
        hostFetchCount += 1;
        if (hostFetchCount === 1) {
          await new Promise<void>(resolve => { releaseHeldHostFetch = resolve; });
        }
      }
      return route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ flows: filtered, total: filtered.length, limit: 50, offset: 0 }),
      });
    });

    await page.goto('/');
    await authenticate(page);
    await page.getByLabel('Time range').selectOption('5m');
    await expect(page.locator('.flow-item')).toHaveCount(1);

    // Trigger host filter and hold its first request
    await page.getByLabel('Filter by host').fill('api.example.test');
    await expect.poll(() => hostFetchCount).toBe(1);

    // Close and reconnect the socket while the host-filter request is held
    await page.evaluate(() => {
      const sockets = (window as unknown as { __sockets: Array<{ readyState: number; close: () => void }> }).__sockets;
      sockets.filter(s => s.readyState === 1).forEach(s => s.close());
    });
    await page.clock.runFor(1_100);
    await expect.poll(() => hostFetchCount).toBe(2);

    // Release the held first host-filter request, then advance 61s so the 4m-old flow expires
    releaseHeldHostFetch?.();
    await page.clock.fastForward(61_000);
    await expect(page.locator('.flow-item')).toHaveCount(0);
    await expect(page.locator('.flow-count')).toHaveText('Showing 0 of 0 flows');
  });

  test('background flow-page clamp during Tasks load does not block Tasks loading settlement', async ({ page }) => {
    const now = Date.now();
    await page.clock.install({ time: new Date(now) });
    await page.addInitScript(() => {
      const sockets: unknown[] = [];
      (window as unknown as { __sockets: unknown[] }).__sockets = sockets;
      class FakeSocket {
        static OPEN = 1;
        readyState = 1;
        onopen?: (ev: unknown) => void;
        onmessage?: (ev: { data: string }) => void;
        constructor() {
          sockets.push(this);
          setTimeout(() => this.onopen?.({}), 0);
        }
        close() { this.readyState = 3; }
        send() {}
      }
      (window as unknown as { WebSocket: unknown }).WebSocket = FakeSocket;
    });

    const dataset = Array.from({ length: 51 }, (_, i) => ({
      ...mockFlows[0],
      id: `f-${i}`,
      timestamp: new Date(now - (i < 50 ? 120_000 : 240_000)).toISOString(),
    }));
    const oldFlow = {
      ...mockFlows[1],
      id: 'flow-ancient',
      timestamp: new Date(now - 600_000).toISOString(),
    };

    let releaseTasksFetch: (() => void) | null = null;
    let tasksRequested = false;

    await page.route('**/api/flows?*', route => {
      const url = new URL(route.request().url());
      const start = Date.parse(url.searchParams.get('start_time') ?? '1970-01-01T00:00:00Z');
      const filtered = dataset.filter(f => Date.parse(f.timestamp) >= start);
      const offset = Number(url.searchParams.get('offset') ?? 0);
      return route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ flows: filtered.slice(offset, offset + 50), total: filtered.length, limit: 50, offset }),
      });
    });

    await page.route('**/api/analytics/tasks*', async route => {
      tasksRequested = true;
      await new Promise<void>(resolve => { releaseTasksFetch = resolve; });
      return route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify([]),
      });
    });

    await page.goto('/');
    await authenticate(page);
    await page.getByLabel('Time range').selectOption('5m');
    await page.getByRole('button', { name: 'Page 2' }).click();
    await expect(page.locator('.flow-count')).toHaveText('Showing 51–51 of 51 flows');

    // Switch to Tasks and hold its request
    await page.getByRole('button', { name: 'Tasks' }).click();
    await expect.poll(() => tasksRequested).toBe(true);

    // Advance 61s and deliver an old WS completion so background flow page 2 clamps to page 1
    await page.clock.fastForward(61_000);
    await page.evaluate(f => {
      const sockets = (window as unknown as { __sockets: Array<{ readyState: number; onmessage?: (ev: { data: string }) => void }> }).__sockets;
      sockets.filter(s => s.readyState === 1).forEach(s => s.onmessage?.({ data: JSON.stringify({ type: 'flow_complete', data: f }) }));
    }, oldFlow);

    // Release Tasks response and verify loading clears to empty state
    releaseTasksFetch?.();
    await expect(page.getByText('No tasks tracked yet')).toBeVisible();
  });
});

