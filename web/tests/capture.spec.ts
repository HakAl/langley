import { test, expect, Page, WebSocketRoute } from '@playwright/test';
import { mockCaptureStatus, setupMocks } from './fixtures';

type CaptureState = 'passthrough' | 'enabling' | 'capturing' | 'stopping';
type CaptureBody = { enabled: boolean; force: boolean };

function status(state: CaptureState, overrides: Record<string, unknown> = {}) {
  return { ...mockCaptureStatus, state, ...overrides };
}

interface CaptureMock {
  posts: CaptureBody[];
  setGet: (body: unknown) => void;
}

/**
 * Overrides the default /api/capture mock. `onPost` decides each POST response;
 * returning `{ status, body }` with a non-200 status simulates a failure.
 */
async function mockCapture(
  page: Page,
  initial: unknown,
  onPost: (body: CaptureBody, n: number) => Promise<{ status?: number; body: unknown }> | { status?: number; body: unknown },
): Promise<CaptureMock> {
  const mock: CaptureMock = { posts: [], setGet: (b) => { getBody = b; } };
  let getBody = initial;
  await page.route('**/api/capture', async (route) => {
    if (route.request().method() === 'GET') {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(getBody) });
      return;
    }
    const body = route.request().postDataJSON() as CaptureBody;
    mock.posts.push(body);
    const res = await onPost(body, mock.posts.length);
    const isJson = typeof res.body !== 'string';
    await route.fulfill({
      status: res.status ?? 200,
      contentType: isJson ? 'application/json' : 'text/plain',
      body: isJson ? JSON.stringify(res.body) : (res.body as string),
    });
  });
  return mock;
}

async function captureWebSocket(page: Page): Promise<() => WebSocketRoute> {
  let server: WebSocketRoute | null = null;
  await page.routeWebSocket('**/ws', (ws) => {
    server = ws;
    ws.onMessage(() => {});
  });
  return () => {
    if (!server) throw new Error('WebSocket not connected');
    return server;
  };
}

function sendCaptureState(ws: WebSocketRoute, data: unknown) {
  ws.send(JSON.stringify({ type: 'capture_state', timestamp: new Date().toISOString(), data }));
}

const control = (page: Page) => page.locator('header .capture-control');

test.describe('Capture control bar', () => {
  test.beforeEach(async ({ page }) => {
    await setupMocks(page);
  });

  test('dormant passthrough -> Start Capture -> capturing', async ({ page }) => {
    const mock = await mockCapture(page, status('passthrough'), () => ({ body: status('capturing') }));
    await page.goto('/');

    await expect(control(page)).toHaveAttribute('data-state', 'passthrough');
    await expect(control(page)).toContainText('Passthrough');
    await expect(control(page)).toContainText('TLS payloads not decrypted or stored');
    const start = page.getByRole('button', { name: 'Start capture' });
    await expect(start).toBeVisible();
    await expect(start).toHaveText('Start Capture');

    await start.click();

    await expect(control(page)).toHaveAttribute('data-state', 'capturing');
    expect(mock.posts).toEqual([{ enabled: true, force: false }]);
    await expect(control(page).locator('.capture-label')).toHaveText('Capturing');
    await expect(control(page)).not.toContainText('Passthrough');
    await expect(page.getByRole('button', { name: 'Pause capture' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Start capture' })).toHaveCount(0);

    const scope = control(page).locator('.capture-scope');
    await expect(scope).toContainText('Intercepting 2 host patterns');
    await expect(scope).toHaveAttribute('title', /api\.anthropic\.com, api\.openai\.com/);
    await expect(scope).toHaveAttribute('title', /all other hosts tunnel through untouched/);
  });

  test('enabling with draining passthrough tunnels -> Force Switch -> capturing', async ({ page }) => {
    const mock = await mockCapture(page, status('passthrough'), (body) =>
      body.force
        ? { body: status('capturing') }
        : { body: status('enabling', { draining_passthrough_tunnels: 2 }) });
    await page.goto('/');

    await page.getByRole('button', { name: 'Start capture' }).click();

    await expect(control(page)).toHaveAttribute('data-state', 'enabling');
    await expect(control(page).locator('.capture-label')).toHaveText(
      'Capturing (2 existing connections will switch when closed — or Force Switch)');
    const forceSwitch = page.getByRole('button', { name: 'Force switch active connections' });
    await expect(forceSwitch).toHaveText('Force Switch');
    await expect(page.getByRole('button', { name: 'Pause capture' })).toBeVisible();

    await forceSwitch.click();

    await expect(control(page)).toHaveAttribute('data-state', 'capturing');
    expect(mock.posts).toEqual([{ enabled: true, force: false }, { enabled: true, force: true }]);
    await expect(control(page).locator('.capture-label')).toHaveText('Capturing');
    await expect(forceSwitch).toHaveCount(0);
  });

  test('enabling label uses singular for one draining connection', async ({ page }) => {
    await mockCapture(page, status('enabling', { draining_passthrough_tunnels: 1 }), () => ({ body: status('capturing') }));
    await page.goto('/');
    await expect(control(page).locator('.capture-label')).toHaveText(
      'Capturing (1 existing connection will switch when closed — or Force Switch)');
  });

  test('Pause Capture shows truthful stopping state until WebSocket reports passthrough', async ({ page }) => {
    const ws = await captureWebSocket(page);
    const mock = await mockCapture(page, status('capturing'), () => ({
      body: status('stopping', { draining_mitm_requests: 1 }),
    }));
    await page.goto('/');
    await expect(page.getByText('Connected')).toBeVisible();

    await page.getByRole('button', { name: 'Pause capture' }).click();

    await expect(control(page)).toHaveAttribute('data-state', 'stopping');
    expect(mock.posts).toEqual([{ enabled: false, force: false }]);
    await expect(control(page).locator('.capture-label')).toHaveText('Stopping capture — finishing 1 active request…');
    await expect(control(page)).not.toContainText('Passthrough');
    await expect(page.getByRole('button', { name: 'Force stop active requests' })).toHaveText('Force Stop');
    await expect(page.getByRole('button', { name: 'Resume capture' })).toBeVisible();

    sendCaptureState(ws(), status('passthrough'));

    await expect(control(page)).toHaveAttribute('data-state', 'passthrough');
    await expect(control(page)).toContainText('TLS payloads not decrypted or stored');
    await expect(page.getByRole('button', { name: 'Start capture' })).toBeVisible();
    expect(mock.posts).toHaveLength(1);
  });

  test('Force Stop aborts draining requests and transitions to passthrough', async ({ page }) => {
    const mock = await mockCapture(page, status('capturing'), (body) =>
      body.force
        ? { body: status('passthrough') }
        : { body: status('stopping', { draining_mitm_requests: 3 }) });
    await page.goto('/');

    await page.getByRole('button', { name: 'Pause capture' }).click();
    await expect(control(page).locator('.capture-label')).toHaveText('Stopping capture — finishing 3 active requests…');

    await page.getByRole('button', { name: 'Force stop active requests' }).click();

    await expect(control(page)).toHaveAttribute('data-state', 'passthrough');
    expect(mock.posts).toEqual([{ enabled: false, force: false }, { enabled: false, force: true }]);
    await expect(control(page).locator('.capture-label')).toHaveText('Passthrough');
  });

  test('Resume Capture from stopping returns to capturing', async ({ page }) => {
    const mock = await mockCapture(page, status('stopping', { draining_mitm_requests: 2 }), () => ({
      body: status('capturing'),
    }));
    await page.goto('/');

    await page.getByRole('button', { name: 'Resume capture' }).click();

    await expect(control(page)).toHaveAttribute('data-state', 'capturing');
    expect(mock.posts).toEqual([{ enabled: true, force: false }]);
  });

  test('WebSocket capture_state broadcasts drive all four states', async ({ page }) => {
    const ws = await captureWebSocket(page);
    await mockCapture(page, status('passthrough'), () => ({ body: status('capturing') }));
    await page.goto('/');
    await expect(page.getByText('Connected')).toBeVisible();
    await expect(control(page)).toHaveAttribute('data-state', 'passthrough');

    sendCaptureState(ws(), status('enabling', { draining_passthrough_tunnels: 4 }));
    await expect(control(page).locator('.capture-label')).toHaveText(
      'Capturing (4 existing connections will switch when closed — or Force Switch)');

    sendCaptureState(ws(), status('capturing', { intercept_hosts: ['api.anthropic.com'] }));
    await expect(control(page).locator('.capture-scope')).toContainText('Intercepting 1 host pattern');

    sendCaptureState(ws(), status('stopping', { draining_mitm_requests: 1 }));
    await expect(control(page)).toHaveAttribute('data-state', 'stopping');

    sendCaptureState(ws(), status('passthrough'));
    await expect(control(page)).toHaveAttribute('data-state', 'passthrough');
  });

  test('refetches authoritative capture state after WebSocket reconnect', async ({ page }) => {
    const ws = await captureWebSocket(page);
    const mock = await mockCapture(page, status('capturing'), () => ({ body: status('capturing') }));
    await page.goto('/');
    await expect(page.getByText('Connected')).toBeVisible();
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');

    // Daemon restarted while disconnected: it comes back in passthrough.
    mock.setGet(status('passthrough'));
    ws().close();
    await expect(page.getByText('Disconnected')).toBeVisible();

    await expect(control(page)).toHaveAttribute('data-state', 'passthrough', { timeout: 5000 });
    await expect(page.getByText('Connected')).toBeVisible();
  });

  test('disables capture buttons while a transition request is in flight', async ({ page }) => {
    let release!: () => void;
    const held = new Promise<void>((resolve) => { release = resolve; });
    const mock = await mockCapture(page, status('enabling', { draining_passthrough_tunnels: 1 }), async () => {
      await held;
      return { body: status('capturing') };
    });
    await page.goto('/');

    const forceSwitch = page.getByRole('button', { name: 'Force switch active connections' });
    const pause = page.getByRole('button', { name: 'Pause capture' });
    await forceSwitch.click();

    await expect(forceSwitch).toBeDisabled();
    await expect(pause).toBeDisabled();
    await expect(control(page)).toHaveAttribute('aria-busy', 'true');
    // A second click on a disabled button must not double-submit.
    await pause.click({ force: true });
    expect(mock.posts).toHaveLength(1);

    release();

    await expect(control(page)).toHaveAttribute('data-state', 'capturing');
    await expect(page.getByRole('button', { name: 'Pause capture' })).toBeEnabled();
    await expect(control(page)).toHaveAttribute('aria-busy', 'false');
    expect(mock.posts).toEqual([{ enabled: true, force: true }]);
  });

  test('shows error banner on 503 and preserves the truthful prior state', async ({ page }) => {
    const mock = await mockCapture(page, status('passthrough'), () => ({
      status: 503,
      body: 'capture control unavailable: proxy not ready',
    }));
    await page.goto('/');

    const start = page.getByRole('button', { name: 'Start capture' });
    await start.click();

    const banner = page.locator('.error-banner');
    await expect(banner).toBeVisible();
    await expect(banner).toContainText('capture control unavailable: proxy not ready');
    expect(mock.posts).toEqual([{ enabled: true, force: false }]);
    await expect(control(page)).toHaveAttribute('data-state', 'passthrough');
    await expect(control(page)).toContainText('TLS payloads not decrypted or stored');
    await expect(start).toBeEnabled();
  });

  test('falls back to HTTP status when error response has no body', async ({ page }) => {
    await mockCapture(page, status('capturing'), () => ({ status: 503, body: '' }));
    await page.goto('/');

    await page.getByRole('button', { name: 'Pause capture' }).click();

    await expect(page.locator('.error-banner')).toContainText('HTTP 503');
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');
  });

  test('capture buttons are keyboard operable with a visible focus ring', async ({ page }) => {
    const mock = await mockCapture(page, status('passthrough'), (body) => ({
      body: status(body.enabled ? 'capturing' : 'passthrough'),
    }));
    await page.goto('/');

    const start = page.getByRole('button', { name: 'Start capture' });
    await expect(start).toBeVisible();

    // Tab until the capture button receives keyboard focus.
    for (let i = 0; i < 40; i++) {
      await page.keyboard.press('Tab');
      if (await start.evaluate((el) => el === document.activeElement)) break;
    }
    await expect(start).toBeFocused();
    expect(await start.evaluate((el) => el.matches(':focus-visible'))).toBe(true);
    const outline = await start.evaluate((el) => {
      const s = getComputedStyle(el);
      return { style: s.outlineStyle, width: s.outlineWidth };
    });
    expect(outline).toEqual({ style: 'solid', width: '2px' });

    await page.keyboard.press('Enter');
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');
    expect(mock.posts).toEqual([{ enabled: true, force: false }]);

    const pause = page.getByRole('button', { name: 'Pause capture' });
    await pause.focus();
    await page.keyboard.press('Space');
    await expect(control(page)).toHaveAttribute('data-state', 'passthrough');
    expect(mock.posts).toEqual([{ enabled: true, force: false }, { enabled: false, force: false }]);
  });

  test('capture control is hidden until capture status loads', async ({ page }) => {
    await page.route('**/api/capture', (route) => route.fulfill({ status: 404, body: 'not found' }));
    await page.goto('/');
    await expect(page.locator('.flow-item').first()).toBeVisible();
    await expect(control(page)).toHaveCount(0);
  });

  test('stale in-flight reconnect GET /api/capture does not overwrite newer POST /api/capture state', async ({ page }) => {
    const ws = await captureWebSocket(page);
    let getCount = 0;
    let releaseStaleGet!: () => void;
    const staleGetHeld = new Promise<void>((resolve) => { releaseStaleGet = resolve; });
    let notifyStaleGetInFlight!: () => void;
    const staleGetInFlight = new Promise<void>((resolve) => { notifyStaleGetInFlight = resolve; });

    await page.route('**/api/capture', async (route) => {
      if (route.request().method() === 'GET') {
        getCount += 1;
        if (getCount === 2) {
          notifyStaleGetInFlight();
          await staleGetHeld;
        }
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(status('passthrough', { revision: 1 })),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(status('capturing', { revision: 2 })),
      });
    });

    await page.goto('/');
    await expect(page.getByText('Connected')).toBeVisible();
    await expect(control(page)).toHaveAttribute('data-state', 'passthrough');

    // Trigger WebSocket disconnect + reconnect so a second GET /api/capture starts and hangs.
    ws().close();
    await staleGetInFlight;

    // While the reconnect GET is still in flight, click Start Capture.
    await page.getByRole('button', { name: 'Start capture' }).click();
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');

    // Release the stale GET(passthrough); UI must remain in 'capturing'.
    releaseStaleGet();
    await page.waitForTimeout(100);
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');
    await expect(control(page).locator('.capture-label')).toHaveText('Capturing');
  });

  test('daemon restart with lower revision resets epoch on reconnect and applies subsequent broadcasts', async ({ page }) => {
    const ws = await captureWebSocket(page);
    const mock = await mockCapture(
      page,
      status('capturing', { revision: 5, instance_id: 'daemon-old' }),
      () => ({ body: status('capturing', { revision: 6, instance_id: 'daemon-old' }) }),
    );

    await page.goto('/');
    await expect(page.getByText('Connected')).toBeVisible();
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');

    // Simulate daemon restart: new instance starts in passthrough at revision 0.
    mock.setGet(status('passthrough', { revision: 0, instance_id: 'daemon-new' }));
    ws().close();
    await expect(page.getByText('Disconnected')).toBeVisible();

    await expect(control(page)).toHaveAttribute('data-state', 'passthrough', { timeout: 5000 });
    await expect(page.getByText('Connected')).toBeVisible();

    // New daemon's first WebSocket broadcast has revision 1 (< old revision 5) and must be applied.
    sendCaptureState(ws(), status('capturing', { revision: 1, instance_id: 'daemon-new' }));
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');
  });

  test('delayed POST from retired daemon cannot replace new daemon capture state', async ({ page }) => {
    const ws = await captureWebSocket(page);
    let releaseOldPost!: () => void;
    const oldPostHeld = new Promise<void>((resolve) => { releaseOldPost = resolve; });
    let notifyOldPostInFlight!: () => void;
    const oldPostInFlight = new Promise<void>((resolve) => { notifyOldPostInFlight = resolve; });
    let restarted = false;

    await page.route('**/api/capture', async (route) => {
      if (route.request().method() === 'POST') {
        notifyOldPostInFlight();
        await oldPostHeld;
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(status('passthrough', { revision: 9, instance_id: 'old-daemon' })),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(
          restarted
            ? status('capturing', { revision: 1, instance_id: 'new-daemon' })
            : status('capturing', { revision: 8, instance_id: 'old-daemon' }),
        ),
      });
    });

    await page.goto('/');
    await expect(page.getByText('Connected')).toBeVisible();
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');

    await page.getByRole('button', { name: 'Pause capture' }).click();
    await oldPostInFlight;

    restarted = true;
    ws().close();
    await expect(page.getByText('Disconnected')).toBeVisible();
    await expect(page.getByText('Connected')).toBeVisible();

    releaseOldPost();
    await expect(control(page)).toHaveAttribute('aria-busy', 'false');
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');
  });

  test('same-daemon reconnect during pending POST preserves its successful result', async ({ page }) => {
    const ws = await captureWebSocket(page);
    let releasePost!: () => void;
    const postHeld = new Promise<void>((resolve) => { releasePost = resolve; });
    let notifyPostInFlight!: () => void;
    const postInFlight = new Promise<void>((resolve) => { notifyPostInFlight = resolve; });
    let gets = 0;

    await page.route('**/api/capture', async (route) => {
      if (route.request().method() === 'POST') {
        notifyPostInFlight();
        await postHeld;
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(status('capturing', { revision: 1, instance_id: 'same-daemon' })),
        });
        return;
      }
      gets += 1;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(status('passthrough', { revision: 0, instance_id: 'same-daemon' })),
      });
    });

    await page.goto('/');
    await expect(page.getByText('Connected')).toBeVisible();
    await expect(control(page)).toHaveAttribute('data-state', 'passthrough');

    await page.getByRole('button', { name: 'Start capture', exact: true }).click();
    await postInFlight;

    ws().close();
    await expect.poll(() => gets).toBeGreaterThan(1);
    await page.waitForTimeout(100);

    releasePost();
    await expect(control(page)).toHaveAttribute('aria-busy', 'false');
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');
  });

  test('older unseen daemon GET cannot retire the current daemon', async ({ page }) => {
    const ws = await captureWebSocket(page);
    let releaseSecondGet!: () => void;
    const secondGetHeld = new Promise<void>((resolve) => { releaseSecondGet = resolve; });
    let notifySecondGet!: () => void;
    const secondGetInFlight = new Promise<void>((resolve) => { notifySecondGet = resolve; });
    let phase: 'initial' | 'hold-reconnect-1' | 'reconnect-2' = 'initial';
    let reconnect2Gets = 0;

    await page.route('**/api/capture', async (route) => {
      if (phase === 'hold-reconnect-1') {
        phase = 'reconnect-2';
        notifySecondGet();
        await secondGetHeld;
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(status('passthrough', { revision: 1, instance_id: 'daemon-B' })),
        });
        return;
      }
      if (phase === 'reconnect-2') {
        reconnect2Gets += 1;
      }
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(
          phase === 'initial'
            ? status('passthrough', { revision: 1, instance_id: 'daemon-A' })
            : status('capturing', { revision: 1, instance_id: 'daemon-C' }),
        ),
      });
    });

    await page.goto('/');
    await expect(page.getByText('Connected')).toBeVisible();
    await expect(control(page)).toHaveAttribute('data-state', 'passthrough');

    phase = 'hold-reconnect-1';
    ws().close();
    await secondGetInFlight;
    await expect(page.getByText('Connected')).toBeVisible();

    ws().close();
    await expect.poll(() => reconnect2Gets).toBeGreaterThan(0);
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');

    releaseSecondGet();
    await page.waitForTimeout(100);

    sendCaptureState(ws(), status('capturing', { revision: 1, instance_id: 'daemon-C' }));
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');
  });

  test('new daemon GET superseded by a POST must still reconcile current state', async ({ page }) => {
    const ws = await captureWebSocket(page);
    let releaseGet!: () => void;
    const getHeld = new Promise<void>((resolve) => { releaseGet = resolve; });
    let notifyGet!: () => void;
    const getInFlight = new Promise<void>((resolve) => { notifyGet = resolve; });
    let releasePost!: () => void;
    const postHeld = new Promise<void>((resolve) => { releasePost = resolve; });
    let notifyPost!: () => void;
    const postInFlight = new Promise<void>((resolve) => { notifyPost = resolve; });
    let gets = 0;

    await page.route('**/api/capture', async (route) => {
      if (route.request().method() === 'POST') {
        notifyPost();
        await postHeld;
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(status('capturing', { revision: 1, instance_id: 'daemon-B' })),
        });
        return;
      }
      gets += 1;
      if (gets === 1) {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(status('passthrough', { revision: 0, instance_id: 'daemon-A' })),
        });
        return;
      }
      notifyGet();
      await getHeld;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(status('capturing', { revision: 1, instance_id: 'daemon-B' })),
      });
    });

    await page.goto('/');
    await expect(page.getByText('Connected')).toBeVisible();
    await expect(control(page)).toHaveAttribute('data-state', 'passthrough');

    ws().close();
    await getInFlight;

    await page.getByRole('button', { name: 'Start capture', exact: true }).click();
    await postInFlight;

    releasePost();
    await expect(control(page)).toHaveAttribute('aria-busy', 'false');

    releaseGet();
    await page.waitForTimeout(100);
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');
  });

  test('new daemon no-op pause during reconnect must reconcile without a WS change', async ({ page }) => {
    const ws = await captureWebSocket(page);
    let releaseGet!: () => void;
    const getHeld = new Promise<void>((resolve) => { releaseGet = resolve; });
    let notifyGet!: () => void;
    const getInFlight = new Promise<void>((resolve) => { notifyGet = resolve; });
    let releasePost!: () => void;
    const postHeld = new Promise<void>((resolve) => { releasePost = resolve; });
    let notifyPost!: () => void;
    const postInFlight = new Promise<void>((resolve) => { notifyPost = resolve; });
    let gets = 0;

    await page.route('**/api/capture', async (route) => {
      if (route.request().method() === 'POST') {
        notifyPost();
        await postHeld;
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(status('passthrough', { revision: 0, instance_id: 'daemon-B' })),
        });
        return;
      }
      gets += 1;
      if (gets === 1) {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(status('capturing', { revision: 3, instance_id: 'daemon-A' })),
        });
        return;
      }
      notifyGet();
      await getHeld;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(status('passthrough', { revision: 0, instance_id: 'daemon-B' })),
      });
    });

    await page.goto('/');
    await expect(page.getByText('Connected')).toBeVisible();
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');

    ws().close();
    await getInFlight;

    await page.getByRole('button', { name: 'Pause capture', exact: true }).click();
    await postInFlight;

    releaseGet();
    await page.waitForTimeout(100);
    releasePost();
    await expect(control(page)).toHaveAttribute('aria-busy', 'false');
    await expect(control(page)).toHaveAttribute('data-state', 'passthrough');
  });

  test('newer reconnect GET must survive completion of the older GET', async ({ page }) => {
    const ws = await captureWebSocket(page);
    let releaseOlder!: () => void;
    const olderHeld = new Promise<void>((resolve) => { releaseOlder = resolve; });
    let notifyOlder!: () => void;
    const olderInFlight = new Promise<void>((resolve) => { notifyOlder = resolve; });
    let releaseNewer!: () => void;
    const newerHeld = new Promise<void>((resolve) => { releaseNewer = resolve; });
    let notifyNewer!: () => void;
    const newerInFlight = new Promise<void>((resolve) => { notifyNewer = resolve; });
    let gets = 0;

    await page.route('**/api/capture', async (route) => {
      gets += 1;
      if (gets === 1) {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(status('passthrough', { revision: 1, instance_id: 'daemon-A' })),
        });
        return;
      }
      if (gets === 2) {
        notifyOlder();
        await olderHeld;
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(status('passthrough', { revision: 1, instance_id: 'daemon-B' })),
        });
        return;
      }
      notifyNewer();
      await newerHeld;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(status('capturing', { revision: 1, instance_id: 'daemon-C' })),
      });
    });

    await page.goto('/');
    await expect(page.getByText('Connected')).toBeVisible();
    await expect(control(page)).toHaveAttribute('data-state', 'passthrough');

    ws().close();
    await olderInFlight;
    await expect(page.getByText('Connected')).toBeVisible();

    ws().close();
    await newerInFlight;

    releaseOlder();
    await page.waitForTimeout(100);
    releaseNewer();
    await page.waitForTimeout(100);
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');
  });

  test('unseen intermediate POST cannot retire the newer current daemon', async ({ page }) => {
    const ws = await captureWebSocket(page);
    let releasePost!: () => void;
    const postHeld = new Promise<void>((resolve) => { releasePost = resolve; });
    let notifyPost!: () => void;
    const postInFlight = new Promise<void>((resolve) => { notifyPost = resolve; });
    let restarted = false;
    let newGets = 0;

    await page.route('**/api/capture', async (route) => {
      if (route.request().method() === 'POST') {
        notifyPost();
        await postHeld;
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(status('passthrough', { revision: 0, instance_id: 'intermediate-daemon' })),
        });
        return;
      }
      if (restarted) {
        newGets += 1;
      }
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(
          restarted
            ? status('capturing', { revision: 1, instance_id: 'new-daemon' })
            : status('capturing', { revision: 8, instance_id: 'old-daemon' }),
        ),
      });
    });

    await page.goto('/');
    await expect(page.getByText('Connected')).toBeVisible();
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');

    await page.getByRole('button', { name: 'Pause capture', exact: true }).click();
    await postInFlight;

    restarted = true;
    ws().close();
    await expect.poll(() => newGets).toBeGreaterThan(0);
    await page.waitForTimeout(150);

    releasePost();
    await expect(control(page)).toHaveAttribute('aria-busy', 'false');

    ws().send(JSON.stringify({
      type: 'capture_state',
      timestamp: new Date().toISOString(),
      data: status('capturing', { revision: 2, instance_id: 'new-daemon' }),
    }));
    await page.waitForTimeout(100);
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');
  });

  test('older unseen GET cannot retire daemon learned from newer WS', async ({ page }) => {
    const ws = await captureWebSocket(page);
    let releaseHeld!: () => void;
    const heldPromise = new Promise<void>((resolve) => { releaseHeld = resolve; });
    let notifyHeld!: () => void;
    const heldInFlight = new Promise<void>((resolve) => { notifyHeld = resolve; });
    let gets = 0;

    await page.route('**/api/capture', async (route) => {
      gets += 1;
      if (gets === 2) {
        notifyHeld();
        await heldPromise;
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(status('passthrough', { revision: 1, instance_id: 'B' })),
        });
        return;
      }
      if (gets > 2) {
        await route.fulfill({ status: 503, body: 'unavailable' });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(status('passthrough', { revision: 1, instance_id: 'A' })),
      });
    });

    await page.goto('/');
    await expect(page.getByText('Connected')).toBeVisible();
    await expect(control(page)).toHaveAttribute('data-state', 'passthrough');

    ws().close();
    await heldInFlight;
    await expect(page.getByText('Connected')).toBeVisible();

    ws().close();
    await expect.poll(() => gets).toBeGreaterThan(2);
    ws().send(JSON.stringify({
      type: 'capture_state',
      timestamp: new Date().toISOString(),
      data: status('capturing', { revision: 1, instance_id: 'C' }),
    }));
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');

    releaseHeld();
    await page.waitForTimeout(150);
    ws().send(JSON.stringify({
      type: 'capture_state',
      timestamp: new Date().toISOString(),
      data: status('capturing', { revision: 1, instance_id: 'C' }),
    }));
    await page.waitForTimeout(150);
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');
  });

  test('failed POST must not discard the only current reconnect snapshot', async ({ page }) => {
    const ws = await captureWebSocket(page);
    let releaseHeld!: () => void;
    const heldPromise = new Promise<void>((resolve) => { releaseHeld = resolve; });
    let notifyHeld!: () => void;
    const heldInFlight = new Promise<void>((resolve) => { notifyHeld = resolve; });
    let releasePost!: () => void;
    const postPromise = new Promise<void>((resolve) => { releasePost = resolve; });
    let notifyPost!: () => void;
    const postInFlight = new Promise<void>((resolve) => { notifyPost = resolve; });
    let gets = 0;

    await page.route('**/api/capture', async (route) => {
      if (route.request().method() === 'POST') {
        notifyPost();
        await postPromise;
        await route.fulfill({
          status: 503,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'unavailable' }),
        });
        return;
      }
      gets += 1;
      if (gets === 2) {
        notifyHeld();
        await heldPromise;
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(status('passthrough', { revision: 2, instance_id: 'A' })),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(
          status(gets === 1 ? 'capturing' : 'passthrough', {
            revision: gets === 1 ? 1 : 2,
            instance_id: 'A',
          }),
        ),
      });
    });

    await page.goto('/');
    await expect(page.getByText('Connected')).toBeVisible();
    await expect(control(page)).toHaveAttribute('data-state', 'capturing');

    ws().close();
    await heldInFlight;

    await page.getByRole('button', { name: 'Pause capture', exact: true }).click();
    await postInFlight;

    releaseHeld();
    await page.waitForTimeout(100);
    releasePost();
    await expect(control(page)).toHaveAttribute('aria-busy', 'false');
    await expect(control(page)).toHaveAttribute('data-state', 'passthrough');
  });
});
