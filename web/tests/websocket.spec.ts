import { test, expect } from '@playwright/test';
import { mockFlows, setupMocks, authenticate } from './fixtures';

test.describe('WebSocket Real-time Updates', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page);
  });

  test('shows Connected when WS opens', async ({ page }) => {
    await page.routeWebSocket('**/ws', (ws) => {
      ws.onMessage(() => {}); // keep alive
    });
    await page.goto('/');
    await authenticate(page);

    await expect(page.locator('.status-dot.connected')).toBeVisible();
    await expect(page.getByText('Connected')).toBeVisible();
  });

  test('shows Disconnected when WS closes', async ({ page }) => {
    let serverWs: { close: () => void } | null = null;
    await page.routeWebSocket('**/ws', (ws) => {
      serverWs = ws;
      ws.onMessage(() => {});
    });
    await page.goto('/');
    await authenticate(page);

    await expect(page.getByText('Connected')).toBeVisible();

    serverWs!.close();

    await expect(page.getByText('Disconnected')).toBeVisible();
  });

  test('adds flow on flow_complete', async ({ page }) => {
    await setupMocks(page, { emptyFlows: true });
    await page.routeWebSocket('**/ws', (ws) => {
      ws.onMessage(() => {});
      // Send a flow_complete message after connection opens
      const newFlow = {
        id: 'ws-flow-1',
        host: 'api.anthropic.com',
        method: 'POST',
        path: '/v1/messages',
        status_code: 200,
        status_text: 'OK',
        is_sse: false,
        timestamp: new Date().toISOString(),
        input_tokens: 100,
        output_tokens: 50,
        total_cost: 0.01,
        flow_integrity: 'complete',
      };
      // Use setTimeout so it sends after the client is ready
      setTimeout(() => {
        ws.send(JSON.stringify({ type: 'flow_complete', timestamp: new Date().toISOString(), data: newFlow }));
      }, 100);
    });
    await page.goto('/');
    await authenticate(page);

    // The new flow should appear in the list
    await expect(page.getByText('api.anthropic.com')).toBeVisible({ timeout: 5000 });
  });

  test('updates flow on flow_update', async ({ page }) => {
    await page.routeWebSocket('**/ws', (ws) => {
      ws.onMessage(() => {});
      // Send an update to flow-1 changing status_code to 500
      setTimeout(() => {
        ws.send(JSON.stringify({
          type: 'flow_update',
          timestamp: new Date().toISOString(),
          data: { ...mockFlows[0], status_code: 500, status_text: 'Internal Server Error' },
        }));
      }, 100);
    });
    await page.goto('/');
    await authenticate(page);

    // Wait for the initial flow, then the update
    await expect(page.locator('.flow-item').first().locator('.status-code')).toContainText('500', { timeout: 5000 });
  });

  test('adds in-progress flow on flow_start', async ({ page }) => {
    await setupMocks(page, { emptyFlows: true });
    await page.routeWebSocket('**/ws', (ws) => {
      ws.onMessage(() => {});
      setTimeout(() => {
        ws.send(JSON.stringify({
          type: 'flow_start',
          timestamp: new Date().toISOString(),
          data: {
            id: 'ws-flow-start',
            host: 'api.cohere.com',
            method: 'POST',
            path: '/v1/generate',
            is_sse: false,
            timestamp: new Date().toISOString(),
          },
        }));
      }, 100);
    });
    await page.goto('/');
    await authenticate(page);

    // The in-progress flow should appear (no status code, shows "...")
    await expect(page.getByText('api.cohere.com')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.flow-item').first().locator('.status-code')).toContainText('...');
  });

  test('handles newline-delimited JSON', async ({ page }) => {
    await setupMocks(page, { emptyFlows: true });
    await page.routeWebSocket('**/ws', (ws) => {
      ws.onMessage(() => {});
      setTimeout(() => {
        const msg1 = JSON.stringify({
          type: 'flow_complete',
          timestamp: new Date().toISOString(),
          data: {
            id: 'ndjson-1',
            host: 'api.anthropic.com',
            method: 'POST',
            path: '/v1/messages',
            status_code: 200,
            is_sse: false,
            timestamp: new Date().toISOString(),
            flow_integrity: 'complete',
          },
        });
        const msg2 = JSON.stringify({
          type: 'flow_complete',
          timestamp: new Date().toISOString(),
          data: {
            id: 'ndjson-2',
            host: 'api.openai.com',
            method: 'POST',
            path: '/v1/chat/completions',
            status_code: 200,
            is_sse: false,
            timestamp: new Date().toISOString(),
            flow_integrity: 'complete',
          },
        });
        // Send as newline-delimited JSON in one frame
        ws.send(msg1 + '\n' + msg2);
      }, 100);
    });
    await page.goto('/');
    await authenticate(page);

    await expect(page.getByText('api.anthropic.com')).toBeVisible({ timeout: 5000 });
    await expect(page.getByText('api.openai.com')).toBeVisible({ timeout: 5000 });
  });

  test('shows error banner after max reconnect failures', async ({ page }) => {
    test.setTimeout(90000); // Exponential backoff: 1s+2s+4s+8s+16s = 31s minimum

    await page.routeWebSocket('**/ws', (ws) => {
      // Close immediately each time to trigger reconnect
      ws.close();
    });

    await page.goto('/');
    await authenticate(page);

    // The hook uses exponential backoff: 1s, 2s, 4s, 8s, 16s
    // After 5 closes, MAX_RECONNECT_ATTEMPTS is reached
    await expect(page.getByText('Connection lost. Refresh to reconnect.')).toBeVisible({ timeout: 60000 });
  });

  test('reconnect refetches flows', async ({ page }) => {
    let serverWs: { close: () => void } | null = null;
    let wsConnectionCount = 0;
    let flowsFetchCount = 0;

    // Count flow API requests
    await page.route('**/api/flows?*', async (route) => {
      flowsFetchCount++;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(mockFlows),
      });
    });

    // Override the other mock routes (stats, etc.) so setupMocks doesn't conflict
    await page.route('**/api/stats**', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
    });
    await page.route('**/api/settings', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
    });

    await page.routeWebSocket('**/ws', (ws) => {
      wsConnectionCount++;
      serverWs = ws;
      ws.onMessage(() => {});
    });

    await page.goto('/');
    await authenticate(page);

    await expect(page.getByText('Connected')).toBeVisible();
    const initialFetches = flowsFetchCount;

    // Close and let reconnect happen
    serverWs!.close();
    await expect(page.getByText('Connected')).toBeVisible({ timeout: 10000 });

    // After reconnect, flows should have been refetched
    expect(flowsFetchCount).toBeGreaterThan(initialFetches);
  });
});
