import { Page } from '@playwright/test';

// --- Mock data constants ---

export const mockFlows = [
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

export const mockFlowsExtended = [
  {
    id: 'flow-ext-1',
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
    provider: 'anthropic',
    flow_integrity: 'complete',
  },
  {
    id: 'flow-ext-2',
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
  {
    id: 'flow-ext-3',
    host: 'api.anthropic.com',
    method: 'POST',
    path: '/v1/messages',
    url: 'https://api.anthropic.com/v1/messages',
    status_code: 500,
    status_text: 'Internal Server Error',
    is_sse: false,
    timestamp: new Date(Date.now() - 120000).toISOString(),
    duration_ms: 50,
    task_id: 'task-xyz',
    model: 'claude-sonnet-4-20250514',
    input_tokens: 500,
    output_tokens: 0,
    total_cost: 0,
    provider: 'anthropic',
    flow_integrity: 'complete',
  },
  {
    id: 'flow-ext-4',
    host: 'api.cohere.com',
    method: 'POST',
    path: '/v1/generate',
    url: 'https://api.cohere.com/v1/generate',
    status_code: 200,
    status_text: 'OK',
    is_sse: false,
    timestamp: new Date(Date.now() - 180000).toISOString(),
    duration_ms: 300,
    task_id: 'task-abc123',
    model: 'command-r',
    input_tokens: 200,
    output_tokens: 100,
    total_cost: 0.005,
    provider: 'cohere',
    flow_integrity: 'complete',
  },
];

export const mockStats = {
  status: 'ok',
  total_flows: 150,
  total_cost: 12.50,
  total_tokens_in: 50000,
  total_tokens_out: 25000,
  total_tasks: 10,
  total_tool_calls: 45,
  avg_cost_per_flow: 0.083,
};

export const mockTasks = [
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

export const mockTools = [
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

export const mockAnomalies = [
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

export const mockDailyCosts = [
  { period: '2026-01-22', flow_count: 50, total_cost: 4.00, total_tokens_in: 20000, total_tokens_out: 10000 },
  { period: '2026-01-23', flow_count: 60, total_cost: 5.00, total_tokens_in: 25000, total_tokens_out: 12000 },
  { period: '2026-01-24', flow_count: 40, total_cost: 3.50, total_tokens_in: 15000, total_tokens_out: 8000 },
];

export const mockSettings = {
  idle_gap_minutes: 5,
};

// --- Helpers ---

export interface SetupMocksOptions {
  emptyFlows?: boolean;
  emptyAnomalies?: boolean;
  flows?: unknown[];
}

export async function setupMocks(page: Page, options: SetupMocksOptions = {}) {
  const flowData = options.flows ?? (options.emptyFlows ? [] : mockFlows);

  await page.route('**/api/flows?*', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(flowData),
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

export async function authenticate(page: Page) {
  await page.evaluate(() => {
    localStorage.setItem('langley_token', 'test-token-123');
  });
  await page.reload();
}
