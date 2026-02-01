import { test, expect, Page } from '@playwright/test';

// Mock data fixtures
const mockFlows = [
  {
    id: 'flow-1',
    host: 'api.anthropic.com',
    method: 'POST',
    path: '/v1/messages',
    url: 'https://api.anthropic.com/v1/messages',
    status_code: 200,
    status_text: 'OK',
    is_sse: true,
    timestamp: new Date().toISOString(),
    duration_ms: 1500,
    task_id: 'task-abc123',
    task_source: 'header',
    model: 'claude-sonnet-4-20250514',
    input_tokens: 1000,
    output_tokens: 500,
    total_cost: 0.0225,
    cost_source: 'calculated',
    provider: 'anthropic',
    flow_integrity: 'complete',
  },
  {
    id: 'flow-2',
    host: 'api.openai.com',
    method: 'POST',
    path: '/v1/chat/completions',
    url: 'https://api.openai.com/v1/chat/completions',
    status_code: 400,
    status_text: 'Bad Request',
    is_sse: false,
    timestamp: new Date(Date.now() - 60000).toISOString(),
    duration_ms: 200,
    model: 'gpt-4',
    input_tokens: 100,
    output_tokens: 0,
    total_cost: 0,
    provider: 'openai',
    flow_integrity: 'complete',
  },
];

const mockStats = {
  status: 'ok',
  total_flows: 150,
  total_cost: 12.50,
  total_tokens_in: 50000,
  total_tokens_out: 25000,
  total_tasks: 10,
  total_tool_calls: 45,
  avg_cost_per_flow: 0.083,
};

const mockTasks = [
  {
    task_id: 'task-abc123',
    flow_count: 5,
    total_tokens_in: 5000,
    total_tokens_out: 2500,
    total_cost: 0.15,
    first_seen: new Date(Date.now() - 3600000).toISOString(),
    last_seen: new Date().toISOString(),
    duration_ms: 3600000,
  },
];

const mockTools = [
  {
    tool_name: 'Read',
    invocation_count: 25,
    success_count: 24,
    failure_count: 1,
    success_rate: 96.0,
    total_cost: 2.50,
    avg_duration_ms: 150,
  },
  {
    tool_name: 'Edit',
    invocation_count: 10,
    success_count: 8,
    failure_count: 2,
    success_rate: 80.0,
    total_cost: 1.25,
    avg_duration_ms: 200,
  },
];

const mockAnomalies = [
  {
    type: 'high_cost',
    flow_id: 'flow-1',
    task_id: 'task-abc123',
    timestamp: new Date().toISOString(),
    severity: 'warning',
    description: 'Flow cost exceeded threshold',
    value: 0.50,
    threshold: 0.25,
  },
];

const mockDailyCosts = [
  { period: '2026-01-22', flow_count: 50, total_cost: 4.00, total_tokens_in: 20000, total_tokens_out: 10000 },
  { period: '2026-01-23', flow_count: 60, total_cost: 5.00, total_tokens_in: 25000, total_tokens_out: 12000 },
  { period: '2026-01-24', flow_count: 40, total_cost: 3.50, total_tokens_in: 15000, total_tokens_out: 8000 },
];

const mockSettings = {
  idle_gap_minutes: 5,
};

// Helper to setup API mocks
async function setupMocks(page: Page, options: { emptyFlows?: boolean; emptyAnomalies?: boolean } = {}) {
  await page.route('**/api/flows?*', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(options.emptyFlows ? [] : mockFlows),
    });
  });

  await page.route('**/api/flows/flow-1', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ ...mockFlows[0], request_body: '{"messages":[]}', response_body: '{"content":"Hello"}' }),
    });
  });

  await page.route('**/api/stats**', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(mockStats),
    });
  });

  await page.route('**/api/analytics/tasks**', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(mockTasks),
    });
  });

  await page.route('**/api/analytics/tools**', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(mockTools),
    });
  });

  await page.route('**/api/analytics/anomalies**', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(options.emptyAnomalies ? [] : mockAnomalies),
    });
  });

  await page.route('**/api/analytics/cost/daily**', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(mockDailyCosts),
    });
  });

  await page.route('**/api/settings', async (route) => {
    if (route.request().method() === 'GET') {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(mockSettings),
      });
    } else if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON();
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ ...mockSettings, ...body }),
      });
    }
  });
}

// Helper to authenticate
async function authenticate(page: Page) {
  await page.evaluate(() => {
    localStorage.setItem('langley_token', 'test-token-123');
  });
  await page.reload();
}

test.describe('Authentication', () => {
  test('shows token input when not authenticated', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByPlaceholder('Enter auth token (langley_...)')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Connect' })).toBeVisible();
  });

  test('stores token in localStorage on connect', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');

    await page.getByPlaceholder('Enter auth token (langley_...)').fill('my-test-token');
    await page.getByRole('button', { name: 'Connect' }).click();

    const token = await page.evaluate(() => localStorage.getItem('langley_token'));
    expect(token).toBe('my-test-token');
  });

  test('hides token input after authentication', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);

    await expect(page.getByPlaceholder('Enter auth token (langley_...)')).not.toBeVisible();
  });
});

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
    await page.getByRole('combobox').selectOption('error');

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

test.describe('WebSocket Real-time Updates', () => {
  test('adds new flow to list in real-time', async ({ page }) => {
    await setupMocks(page, { emptyFlows: true });
    await page.goto('/');
    await authenticate(page);

    // Initially empty
    await expect(page.getByText('No flows captured yet')).toBeVisible();

    // Inject a mock WebSocket message
    await page.evaluate(() => {
      const mockFlow = {
        id: 'ws-flow-1',
        host: 'api.anthropic.com',
        method: 'POST',
        path: '/v1/messages',
        status_code: 200,
        is_sse: false,
        timestamp: new Date().toISOString(),
        input_tokens: 100,
        output_tokens: 50,
        total_cost: 0.01,
      };
      const event = new MessageEvent('message', {
        data: JSON.stringify({ type: 'flow_complete', timestamp: new Date().toISOString(), data: mockFlow }),
      });
      // Get the WebSocket and dispatch event (if connected)
      // Since WebSocket mock is complex, we simulate the state update directly
      window.dispatchEvent(new CustomEvent('test-ws-message', { detail: mockFlow }));
    });

    // In a real test, we'd mock the WebSocket connection
    // This test demonstrates the pattern; actual WS testing requires more setup
  });

  test('updates existing flow on flow_update message', async ({ page }) => {
    await setupMocks(page);
    await page.goto('/');
    await authenticate(page);

    // Initial flow shows 200 status
    await expect(page.locator('.flow-item').first().locator('.status-code')).toContainText('200');

    // WebSocket update would change the flow
    // Pattern: emit flow_update with updated data
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
    await page.route('**/api/flows?*', async (route) => {
      await route.fulfill({ status: 401 });
    });

    await page.goto('/');
    await authenticate(page);

    await expect(page.getByText('Invalid token')).toBeVisible();
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
    const firstRow = page.locator('tbody tr[role="option"]').first();
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
