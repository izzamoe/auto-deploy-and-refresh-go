const { test, expect } = require('@playwright/test');

function baseURL() {
  const value = process.env.PLAYWRIGHT_BASE_URL;
  if (!value) {
    throw new Error('PLAYWRIGHT_BASE_URL is not set');
  }
  return value;
}

function webhookSecret() {
  const value = process.env.PLAYWRIGHT_WEBHOOK_SECRET;
  if (!value) {
    throw new Error('PLAYWRIGHT_WEBHOOK_SECRET is not set');
  }
  return value;
}

async function seedHistoryJob(request) {
  const tag = `pw-history-${Date.now()}`;
  const response = await request.post(`${baseURL()}/webhook`, {
    headers: {
      Authorization: `Bearer ${webhookSecret()}`
    },
    data: { tag }
  });

  expect(response.status()).toBe(202);

  return tag;
}

async function newAuthenticatedContext(browser) {
  return browser.newContext({
    httpCredentials: {
      username: process.env.PLAYWRIGHT_ADMIN_USERNAME,
      password: process.env.PLAYWRIGHT_ADMIN_PASSWORD
    }
  });
}

async function createApp(page, suffix, namePrefix) {
  const appName = `${namePrefix}-${suffix}`;
  const webhookSecret = `${namePrefix}-secret-${suffix}`;
  const serviceName = `${appName}.service`;
  const binaryPath = `/opt/${appName}`;
  const githubRepo = `example/${appName}`;
  const artifactName = `${appName}-artifact`;

  await page.goto(`${baseURL()}/admin/apps#/apps/new`);
  await expect(page.getByRole('heading', { name: 'New App' })).toBeVisible();
  await expect(page.locator('#app-form')).toBeVisible();

  await page.locator('#name').fill(appName);
  await page.locator('#webhook_secret').fill(webhookSecret);
  await page.locator('#service_name').fill(serviceName);
  await page.locator('#binary_path').fill(binaryPath);
  await page.locator('#github_repo').fill(githubRepo);
  await page.locator('#artifact_name').fill(artifactName);

  const [createRequest, createResponse] = await Promise.all([
    page.waitForRequest((req) => req.url().includes('/admin/api/apps') && req.method() === 'POST'),
    page.waitForResponse((res) => res.url().includes('/admin/api/apps') && res.request().method() === 'POST'),
    page.locator('#submit-btn').click()
  ]);

  expect(createRequest.headers()['x-admin-request']).toBe('true');
  expect(createRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(createResponse.status()).toBe(201);

  await expect(page).toHaveURL(/\/admin\/apps#\//);

  const appCard = page.locator('[data-app-id]').filter({ hasText: appName }).first();
  await expect(appCard).toBeVisible();

  return {
    appId: await appCard.getAttribute('data-app-id'),
    appName,
    webhookSecret,
    serviceName,
    binaryPath,
    githubRepo,
    artifactName
  };
}

async function expectNativeAdminRuntime(page) {
  await expect.poll(() => page.evaluate(() => ({
    hasLegacyRuntime: typeof window['ht' + 'mx'] !== 'undefined',
    wsHooks: document.querySelectorAll('[data-admin-ws-url="/admin/events/ws"]').length
  }))).toEqual({ hasLegacyRuntime: false, wsHooks: 1 });
}

async function installAdminUITestWebSocketHook(page) {
  await page.addInitScript(() => {
    const NativeWebSocket = window.WebSocket;
    const sockets = [];
    const sentMessages = [];

    window.WebSocket = function AdminSmokeWebSocket(url, protocols) {
      const socket = protocols === undefined
        ? new NativeWebSocket(url)
        : new NativeWebSocket(url, protocols);
      socket.send = function AdminSmokeSend(data) {
        sentMessages.push({
          url: String(socket.url),
          data: typeof data === 'string' ? data : String(data)
        });
        return NativeWebSocket.prototype.send.call(socket, data);
      };
      sockets.push(socket);
      return socket;
    };
    window.WebSocket.prototype = NativeWebSocket.prototype;
    Object.setPrototypeOf(window.WebSocket, NativeWebSocket);
    window.AdminUITest = {
      injectEvent(data) {
        const socket = sockets.find((candidate) => String(candidate.url).includes('/admin/events/ws'));
        if (socket) {
          socket.dispatchEvent(new MessageEvent('message', { data }));
        }
      },
      getSendCount() {
        return sentMessages.length;
      },
      getSentMessages() {
        return sentMessages.slice();
      }
    };
  });
}

async function getWebSocketSendCount(page) {
  return page.evaluate(() => window.AdminUITest?.getSendCount?.() || 0);
}

async function expectNoWebSocketSends(page, beforeCount) {
  await expect.poll(() => getWebSocketSendCount(page)).toBe(beforeCount);
}

test('admin apps page loads with basic auth', async ({ browser }) => {
  const context = await newAuthenticatedContext(browser);
  const page = await context.newPage();

  await page.goto(`${baseURL()}/admin/apps`);

  await expect(page.getByRole('heading', { name: 'Apps' })).toBeVisible();
  await expectNativeAdminRuntime(page);
  await expect(page.locator('#apps-table')).toBeVisible();
  await expect(page.locator('[data-app-id]')).toHaveCount(1);
  await expect(page.locator('[data-app-id]').first()).toContainText('default');

  await context.close();
});

test('admin apps page rejects requests without auth', async ({ request }) => {
  const response = await request.get(`${baseURL()}/admin/apps`);

  expect(response.status()).toBe(401);
  expect(response.headers()['www-authenticate']).toBe('Basic realm="auto-deploy admin"');
});

test('history navigation supports deep links and retry updates stay in place', async ({ browser, request }) => {
  const tag = await seedHistoryJob(request);
  const context = await newAuthenticatedContext(browser);
  const page = await context.newPage();

  await page.goto(`${baseURL()}/admin/apps`);
  await expect(page.locator('[data-app-id]')).toHaveCount(1);

  const appCard = page.locator('[data-app-id]').first();
  const historyLink = appCard.getByRole('link', { name: 'History' });

  await historyLink.click();
  await expect(page).toHaveURL(/\/admin\/apps#\/history\?appId=/);
  await expect(page.locator('#history-content')).toBeVisible();
  await expectNativeAdminRuntime(page);
  await expect(page.locator('#history-table-region')).toBeVisible();

  const statusRow = page.locator(`#history-table tr:has-text("${tag}")`).first();
  await expect(statusRow).toBeVisible({ timeout: 30000 });
  await expect(statusRow).toContainText(/failed|succeeded/i, { timeout: 30000 });

  await page.reload();
  await expect(page).toHaveURL(/\/admin\/apps#\/history\?appId=/);
  await expect(page.locator('#history-content')).toBeVisible();
  await expect(statusRow).toBeVisible();

  await page.goBack();
  await expect(page).toHaveURL(/\/admin\/apps(#\/)?$/);
  await expect(page.locator('#apps-table')).toBeVisible();

  await page.goForward();
  await expect(page).toHaveURL(/\/admin\/apps#\/history\?appId=/);
  await expect(page.locator('#history-content')).toBeVisible();

  await expect(statusRow).toBeVisible({ timeout: 30000 });
  await expect(statusRow).toContainText(/failed|succeeded/i, { timeout: 30000 });
  const retryButton = statusRow.locator('.retry-button');
  await expect(retryButton).toBeVisible();
  await retryButton.click();

  await expect(page).toHaveURL(/\/admin\/apps#\/history\?appId=/);
  await expect(page.getByTestId('admin-flash')).toContainText('Retry queued');
  await expect(page.locator('[data-admin-ws-url="/admin/events/ws"]')).toHaveCount(1);
  await expect(page.locator('#history-table tr[data-status="pending"]')).toContainText(tag);
  const historyNavigationCount = await page.evaluate(() => performance.getEntriesByType('navigation').length);
  await expect(page.locator('#history-table tr[data-status="pending"]')).toContainText(tag);
  await expect(page.locator('#history-table tr[data-status="pending"]')).toHaveCount(0, { timeout: 30000 });
  await expect(page.locator('#history-table tr[data-status="failed"], #history-table tr[data-status="succeeded"]').filter({ hasText: tag })).toHaveCount(2, { timeout: 30000 });
  expect(await page.evaluate(() => performance.getEntriesByType('navigation').length)).toBe(historyNavigationCount);
  await expect(page.locator(`#history-table tr:has-text("${tag}")`)).toHaveCount(2, { timeout: 30000 });

  await context.close();
});

test('apps list deploy settles live through WebSocket without reload', async ({ browser }) => {
  const context = await newAuthenticatedContext(browser);
  const page = await context.newPage();
  const tag = `pw-apps-${Date.now()}`;

  await page.goto(`${baseURL()}/admin/apps`);
  await expect(page.locator('[data-app-id]')).toHaveCount(1);
  await expectNativeAdminRuntime(page);

  const appCard = page.locator('[data-app-id]').first();
  await appCard.locator('input[name="tag"]').fill(tag);
  await appCard.getByRole('button', { name: 'Deploy', exact: true }).click();

  await expect(page.getByTestId('admin-flash')).toContainText(`Manual deploy queued for ${tag}`);
  await expect(page.locator('[data-admin-ws-url="/admin/events/ws"]')).toHaveCount(1);

  const statusBadge = appCard.locator('[data-progress-region] .deploy-status-badge');
  await expect(statusBadge).toContainText(/pending|in_progress|downloading|validating|backing_up|installing|restarting|healthcheck|rollback/i);
  await expect(appCard.locator('[data-progress-live]')).toBeVisible();

  const navigationCount = await page.evaluate(() => performance.getEntriesByType('navigation').length);
  await expect(appCard).not.toHaveAttribute('data-progress-job', /.+/, { timeout: 30000 });
  await expect(statusBadge).toContainText(/failed|succeeded/i, { timeout: 30000 });
  if (await statusBadge.evaluate((node) => node.textContent.toLowerCase().includes('failed'))) {
    await expect(statusBadge).toHaveClass(/status-failed/);
    await expect(appCard.locator('[data-progress-detail]')).toContainText('Deployment failed');
  }
  expect(await page.evaluate(() => performance.getEntriesByType('navigation').length)).toBe(navigationCount);

  await context.close();
});

test('native progress renderer ignores malformed frames and renders failed terminal frames', async ({ browser }) => {
  const context = await newAuthenticatedContext(browser);
  const page = await context.newPage();
  const suffix = `${Date.now()}`;
  const appId = `pw-progress-app-${suffix}`;
  const jobId = `pw-progress-job-${suffix}`;

  await page.route('**/admin/api/apps', async (route) => {
    if (route.request().method() === 'GET') {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          apps: [{
            id: appId,
            name: `playwright-progress-${suffix}`,
            serviceName: `playwright-progress-${suffix}.service`,
            githubRepo: `example/playwright-progress-${suffix}`,
            binaryPath: `/opt/playwright-progress-${suffix}`,
            artifactName: `playwright-progress-${suffix}-artifact`,
            enabled: true,
            lastJobId: jobId,
            lastJobStatus: 'unknown',
            lastDeployStatus: 'Waiting for progress smoke frame'
          }]
        })
      });
      return;
    }

    await route.continue();
  });

  await installAdminUITestWebSocketHook(page);

  await page.goto(`${baseURL()}/admin/apps`);
  await expect(page.locator(`[data-app-id="${appId}"]`)).toHaveCount(1);
  await expectNativeAdminRuntime(page);
  await expect.poll(() => page.evaluate(() => typeof window.AdminUITest?.injectEvent)).toBe('function');

  const appCard = page.locator(`[data-app-id="${appId}"]`);

  const originalStatus = await appCard.locator('[data-progress-region] .deploy-status-badge').textContent();

  await page.evaluate(() => {
    if (window.AdminUITest) {
      window.AdminUITest.injectEvent('not-json-at-all');
    }
  });
  await expect(appCard.locator('[data-progress-region] .deploy-status-badge')).toHaveText(originalStatus.trim());

  await page.evaluate(({ a, j }) => {
    if (window.AdminUITest) {
      window.AdminUITest.injectEvent(JSON.stringify({
        t: 'p',
        a,
        j,
        ph: 'failed',
        st: 'failed',
        pct: 100,
        tb: 4096,
        db: 4096,
        bps: 256,
        msg: 'smoke test failure'
      }));
    }
  }, { a: appId, j: jobId });

  await expect(appCard.locator('[data-progress-region] .deploy-status-badge')).toHaveText('failed');
  await expect(appCard.locator('[data-progress-region] .deploy-status-badge')).toHaveClass(/status-failed/);
  await expect(appCard.locator('[data-progress-detail]')).toContainText('Current activity: Deployment failed');
  await expect(appCard.locator('[data-progress-detail]')).toContainText('Detail: smoke test failure');

  await context.close();
});

test('shadcn admin ui renders realtime speed and status clearly', async ({ browser }) => {
  const context = await newAuthenticatedContext(browser);
  const page = await context.newPage();
  const suffix = `${Date.now()}`;
  const appId = `pw-shadcn-app-${suffix}`;
  const jobId = `pw-boundary-job-${suffix}`;
  const failedJobId = `pw-boundary-failed-${suffix}`;
  const retryJobId = `pw-boundary-retry-${suffix}`;
  const cancelJobId = `pw-boundary-cancel-${suffix}`;
  const tag = `pw-boundary-${suffix}`;
  const initialApp = {
    id: appId,
    name: `playwright-shadcn-existing-${suffix}`,
    serviceName: `playwright-shadcn-existing-${suffix}.service`,
    githubRepo: `example/playwright-shadcn-existing-${suffix}`,
    binaryPath: `/opt/playwright-shadcn-existing-${suffix}`,
    artifactName: `playwright-shadcn-existing-${suffix}-artifact`,
    enabled: true,
    lastJobId: jobId,
    lastJobStatus: 'in_progress',
    lastDeployStatus: 'Deployment in progress'
  };
  const appsPayload = { apps: [initialApp] };
  const historyPayload = {
    history: [{
      id: failedJobId,
      tag,
      status: 'failed',
      trigger: 'manual',
      errorMsg: 'Synthetic smoke history row',
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
      appEnabled: true
    }, {
      id: cancelJobId,
      tag: `${tag}-cancel`,
      status: 'in_progress',
      trigger: 'manual',
      errorMsg: '',
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
      appEnabled: true
    }]
  };

  await page.route('**/admin/api/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const method = request.method();
    const apiPath = url.pathname.replace('/admin/api', '');
    const fulfillJSON = (status, body) => route.fulfill({
      status,
      contentType: 'application/json',
      body: JSON.stringify(body)
    });

    if (apiPath === '/apps' && method === 'GET') {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(appsPayload)
      });
      return;
    }

    if (apiPath === '/apps' && method === 'POST') {
      const body = request.postDataJSON();
      const createdApp = {
        id: `pw-shadcn-created-${suffix}`,
        name: body.name,
        serviceName: body.serviceName,
        githubRepo: body.githubRepo,
        binaryPath: body.binaryPath,
        artifactName: body.artifactName,
        enabled: body.enabled,
        lastJobId: jobId,
        lastJobStatus: 'in_progress',
        lastDeployStatus: 'Deployment in progress'
      };
      appsPayload.apps = [createdApp];
      await fulfillJSON(201, { app: createdApp });
      return;
    }

    const appMatch = apiPath.match(/^\/apps\/([^/]+)(?:\/(deploy|toggle|cancel))?$/);
    if (appMatch && method === 'GET' && !appMatch[2]) {
      const app = appsPayload.apps.find((candidate) => candidate.id === appMatch[1]);
      await fulfillJSON(app ? 200 : 404, app ? { app } : { error: 'not found' });
      return;
    }

    if (appMatch && method === 'PUT' && !appMatch[2]) {
      const body = request.postDataJSON();
      const app = appsPayload.apps.find((candidate) => candidate.id === appMatch[1]);
      if (!app) {
        await fulfillJSON(404, { error: 'not found' });
        return;
      }
      Object.assign(app, {
        name: body.name,
        serviceName: body.serviceName,
        githubRepo: body.githubRepo,
        binaryPath: body.binaryPath,
        artifactName: body.artifactName,
        enabled: body.enabled
      });
      await fulfillJSON(200, { app });
      return;
    }

    if (appMatch && method === 'POST' && appMatch[2] === 'deploy') {
      const app = appsPayload.apps.find((candidate) => candidate.id === appMatch[1]);
      if (app) {
        app.lastJobId = jobId;
        app.lastJobStatus = 'in_progress';
        app.lastDeployStatus = 'Deployment in progress';
      }
      await fulfillJSON(202, { status: 'ok' });
      return;
    }

    if (appMatch && method === 'POST' && appMatch[2] === 'toggle') {
      const body = request.postDataJSON();
      const app = appsPayload.apps.find((candidate) => candidate.id === appMatch[1]);
      if (!app) {
        await fulfillJSON(404, { error: 'not found' });
        return;
      }
      app.enabled = body.enabled;
      await fulfillJSON(200, { app });
      return;
    }

    if (appMatch && method === 'POST' && appMatch[2] === 'cancel') {
      const app = appsPayload.apps.find((candidate) => candidate.id === appMatch[1]);
      if (app) {
        app.lastJobStatus = 'cancel_requested';
        app.lastDeployStatus = 'Cancel requested';
      }
      await fulfillJSON(202, { status: 'ok' });
      return;
    }

    if (appMatch && method === 'DELETE' && !appMatch[2]) {
      appsPayload.apps = appsPayload.apps.filter((candidate) => candidate.id !== appMatch[1]);
      await fulfillJSON(200, { status: 'ok' });
      return;
    }

    if (apiPath === '/history' && method === 'GET') {
      await fulfillJSON(200, historyPayload);
      return;
    }

    const retryMatch = apiPath.match(/^\/history\/([^/]+)\/retry$/);
    if (retryMatch && method === 'POST') {
      const originalJob = historyPayload.history.find((candidate) => candidate.id === retryMatch[1]);
      if (!originalJob) {
        await fulfillJSON(404, { error: 'not found' });
        return;
      }
      const retryJob = {
        ...originalJob,
        id: retryJobId,
        status: 'pending',
        trigger: 'manual_retry',
        errorMsg: '',
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString()
      };
      historyPayload.history = [retryJob, ...historyPayload.history];
      await fulfillJSON(202, { jobId: retryJobId });
      return;
    }

    const jobCancelMatch = apiPath.match(/^\/jobs\/([^/]+)\/cancel$/);
    if (jobCancelMatch && method === 'POST') {
      const job = historyPayload.history.find((candidate) => candidate.id === jobCancelMatch[1]);
      if (job) {
        job.status = 'cancel_requested';
      }
      await fulfillJSON(202, { status: 'ok' });
      return;
    }

    await route.continue();
  });

  await installAdminUITestWebSocketHook(page);

  const realtimeSocketPromise = page.waitForEvent('websocket', {
    predicate: (socket) => socket.url().includes('/admin/events/ws')
  });

  await page.goto(`${baseURL()}/admin/apps`);
  const realtimeSocket = await realtimeSocketPromise;
  expect(new URL(realtimeSocket.url()).pathname).toBe('/admin/events/ws');
  expect(realtimeSocket.url()).not.toContain('/admin/api');
  await expectNativeAdminRuntime(page);
  await expect.poll(() => page.evaluate(() => typeof window.AdminUITest?.injectEvent)).toBe('function');
  await expect(page.getByRole('heading', { name: 'Apps' })).toBeVisible();
  await expect(page.locator('#apps-table')).toBeVisible();

  let wsSendCount = await getWebSocketSendCount(page);
  const createdApp = await createApp(page, suffix, 'playwright-shadcn-smoke');
  await expectNoWebSocketSends(page, wsSendCount);

  let appCard = page.locator(`[data-app-id="${createdApp.appId}"]`);
  await expect(appCard).toBeVisible();

  await appCard.getByRole('link', { name: 'Edit' }).click();
  await expect(page).toHaveURL(/\/admin\/apps#\/apps\/[^/]+\/edit$/);
  await expect(page.getByRole('heading', { name: 'Edit App' })).toBeVisible();
  await expect(page.locator('#app-form')).toBeVisible();

  await page.locator('#artifact_name').fill(`${createdApp.artifactName}-updated`);
  wsSendCount = await getWebSocketSendCount(page);
  const [updateRequest, updateResponse] = await Promise.all([
    page.waitForRequest((request) => request.url().includes(`/admin/api/apps/${createdApp.appId}`) && request.method() === 'PUT'),
    page.waitForResponse((response) => response.url().includes(`/admin/api/apps/${createdApp.appId}`) && response.request().method() === 'PUT'),
    page.locator('#submit-btn').click()
  ]);

  expect(new URL(updateRequest.url()).pathname).toBe(`/admin/api/apps/${createdApp.appId}`);
  expect(updateRequest.url()).not.toContain('/admin/events/ws');
  expect(updateRequest.headers()['x-admin-request']).toBe('true');
  expect(updateRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(updateResponse.status()).toBe(200);
  await expectNoWebSocketSends(page, wsSendCount);
  await expect(page).toHaveURL(/\/admin\/apps#\//);

  appCard = page.locator(`[data-app-id="${createdApp.appId}"]`);
  await expect(appCard).toBeVisible();
  await appCard.getByRole('link', { name: 'History' }).click();
  await expect(page).toHaveURL(/\/admin\/apps#\/history\?appId=/);
  await expect(page.locator('#history-content')).toBeVisible();
  await expect(page.locator('#history-table-region')).toBeVisible();
  await expect(page.locator('#history-table')).toBeVisible();

  await page.getByRole('link', { name: 'Apps' }).click();
  await expect(page).toHaveURL(/\/admin\/apps#\//);

  appCard = page.locator(`[data-app-id="${createdApp.appId}"]`);
  await expect(appCard).toBeVisible();

  await appCard.locator('input[name="tag"]').fill(tag);

  wsSendCount = await getWebSocketSendCount(page);
  const [deployRequest, deployResponse] = await Promise.all([
    page.waitForRequest((request) => request.url().includes(`/admin/api/apps/${createdApp.appId}/deploy`) && request.method() === 'POST'),
    page.waitForResponse((response) => response.url().includes(`/admin/api/apps/${createdApp.appId}/deploy`) && response.request().method() === 'POST'),
    appCard.getByRole('button', { name: 'Deploy', exact: true }).click()
  ]);

  expect(new URL(deployRequest.url()).pathname).toBe(`/admin/api/apps/${createdApp.appId}/deploy`);
  expect(deployRequest.url()).not.toContain('/admin/events/ws');
  expect(deployRequest.headers()['x-admin-request']).toBe('true');
  expect(deployRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(deployResponse.status()).toBe(202);
  await expectNoWebSocketSends(page, wsSendCount);
  await expect(page.getByTestId('admin-flash')).toContainText(`Manual deploy queued for ${tag}`);

  await page.evaluate(({ a, j }) => {
    if (window.AdminUITest) {
      window.AdminUITest.injectEvent(JSON.stringify({
        t: 'p',
        a,
        j,
        ph: 'downloading',
        st: 'in_progress',
        pct: 50,
        tb: 1048576,
        db: 524288,
        bps: 524288
      }));
    }
  }, { a: createdApp.appId, j: jobId });

  await expect(appCard.locator('[data-progress-detail]')).toContainText('Current activity: Downloading release artifact');
  await expect(appCard.locator('[data-progress-speed]')).toContainText('512 KB/s');

  await page.evaluate(({ a, j }) => {
    if (window.AdminUITest) {
      window.AdminUITest.injectEvent(JSON.stringify({
        t: 'p',
        a,
        j,
        ph: 'downloading',
        st: 'in_progress',
        pct: 100,
        tb: 1048576,
        db: 1048576,
        bps: 1048576
      }));
    }
  }, { a: createdApp.appId, j: jobId });

  await expect(appCard.locator('[data-progress-detail]')).toContainText('Current activity: Downloading release artifact');
  await expect(appCard.locator('[data-progress-speed]')).toContainText('1.0 MB/s');

  wsSendCount = await getWebSocketSendCount(page);
  await appCard.getByRole('button', { name: 'Cancel Deploy' }).click();
  await expect(page.getByText('Cancel all pending/running deployments for this app?')).toBeVisible();
  const [appCancelRequest, appCancelResponse] = await Promise.all([
    page.waitForRequest((request) => request.url().includes(`/admin/api/apps/${createdApp.appId}/cancel`) && request.method() === 'POST'),
    page.waitForResponse((response) => response.url().includes(`/admin/api/apps/${createdApp.appId}/cancel`) && response.request().method() === 'POST'),
    page.getByRole('button', { name: 'Confirm cancel deploy' }).click()
  ]);

  expect(new URL(appCancelRequest.url()).pathname).toBe(`/admin/api/apps/${createdApp.appId}/cancel`);
  expect(appCancelRequest.url()).not.toContain('/admin/events/ws');
  expect(appCancelRequest.headers()['x-admin-request']).toBe('true');
  expect(appCancelRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(appCancelResponse.status()).toBe(202);
  await expectNoWebSocketSends(page, wsSendCount);
  await expect(page.getByTestId('admin-flash')).toContainText('Cancel requested');

  wsSendCount = await getWebSocketSendCount(page);
  const [disableRequest, disableResponse] = await Promise.all([
    page.waitForRequest((request) => request.url().includes(`/admin/api/apps/${createdApp.appId}/toggle`) && request.method() === 'POST'),
    page.waitForResponse((response) => response.url().includes(`/admin/api/apps/${createdApp.appId}/toggle`) && response.request().method() === 'POST'),
    appCard.getByRole('button', { name: 'Disable' }).click()
  ]);

  expect(new URL(disableRequest.url()).pathname).toBe(`/admin/api/apps/${createdApp.appId}/toggle`);
  expect(disableRequest.url()).not.toContain('/admin/events/ws');
  expect(disableRequest.headers()['x-admin-request']).toBe('true');
  expect(disableRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(disableResponse.status()).toBe(200);
  await expectNoWebSocketSends(page, wsSendCount);
  await expect(page.getByTestId('admin-flash')).toContainText('App disabled successfully');

  appCard = page.locator(`[data-app-id="${createdApp.appId}"]`);
  await expect(appCard).toContainText('Disabled');
  wsSendCount = await getWebSocketSendCount(page);
  const [enableRequest, enableResponse] = await Promise.all([
    page.waitForRequest((request) => request.url().includes(`/admin/api/apps/${createdApp.appId}/toggle`) && request.method() === 'POST'),
    page.waitForResponse((response) => response.url().includes(`/admin/api/apps/${createdApp.appId}/toggle`) && response.request().method() === 'POST'),
    appCard.getByRole('button', { name: 'Enable' }).click()
  ]);

  expect(new URL(enableRequest.url()).pathname).toBe(`/admin/api/apps/${createdApp.appId}/toggle`);
  expect(enableRequest.url()).not.toContain('/admin/events/ws');
  expect(enableRequest.headers()['x-admin-request']).toBe('true');
  expect(enableRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(enableResponse.status()).toBe(200);
  await expectNoWebSocketSends(page, wsSendCount);
  await expect(page.getByTestId('admin-flash')).toContainText('App enabled successfully');

  await appCard.getByRole('link', { name: 'History' }).click();
  await expect(page.locator('#history-content')).toBeVisible();
  await expect(page.locator('#history-table-region')).toBeVisible();
  await expect(page.locator('#history-table')).toBeVisible();

  const retryRow = page.locator(`#history-table tr[data-job-id="${failedJobId}"]`);
  await expect(retryRow).toBeVisible();
  wsSendCount = await getWebSocketSendCount(page);
  const [retryRequest, retryResponse] = await Promise.all([
    page.waitForRequest((request) => request.url().includes(`/admin/api/history/${failedJobId}/retry`) && request.method() === 'POST'),
    page.waitForResponse((response) => response.url().includes(`/admin/api/history/${failedJobId}/retry`) && response.request().method() === 'POST'),
    retryRow.locator('.retry-button').click()
  ]);

  expect(new URL(retryRequest.url()).pathname).toBe(`/admin/api/history/${failedJobId}/retry`);
  expect(retryRequest.url()).not.toContain('/admin/events/ws');
  expect(retryRequest.headers()['x-admin-request']).toBe('true');
  expect(retryRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(retryResponse.status()).toBe(202);
  await expectNoWebSocketSends(page, wsSendCount);
  await expect(page.getByTestId('admin-flash')).toContainText('Retry queued');

  const cancelRow = page.locator(`#history-table tr[data-job-id="${cancelJobId}"]`);
  await expect(cancelRow).toBeVisible();
  wsSendCount = await getWebSocketSendCount(page);
  const [jobCancelRequest, jobCancelResponse] = await Promise.all([
    page.waitForRequest((request) => request.url().includes(`/admin/api/jobs/${cancelJobId}/cancel`) && request.method() === 'POST'),
    page.waitForResponse((response) => response.url().includes(`/admin/api/jobs/${cancelJobId}/cancel`) && response.request().method() === 'POST'),
    cancelRow.getByRole('button', { name: 'Cancel' }).click()
  ]);

  expect(new URL(jobCancelRequest.url()).pathname).toBe(`/admin/api/jobs/${cancelJobId}/cancel`);
  expect(jobCancelRequest.url()).not.toContain('/admin/events/ws');
  expect(jobCancelRequest.headers()['x-admin-request']).toBe('true');
  expect(jobCancelRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(jobCancelResponse.status()).toBe(202);
  await expectNoWebSocketSends(page, wsSendCount);
  await expect(page.getByTestId('admin-flash')).toContainText('Cancel requested');

  await page.getByRole('link', { name: 'Apps' }).click();
  appCard = page.locator(`[data-app-id="${createdApp.appId}"]`);
  await expect(appCard).toBeVisible();
  wsSendCount = await getWebSocketSendCount(page);
  await appCard.getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByText('Are you sure you want to delete this app?')).toBeVisible();
  const [deleteRequest, deleteResponse] = await Promise.all([
    page.waitForRequest((request) => request.url().includes(`/admin/api/apps/${createdApp.appId}`) && request.method() === 'DELETE'),
    page.waitForResponse((response) => response.url().includes(`/admin/api/apps/${createdApp.appId}`) && response.request().method() === 'DELETE'),
    page.getByRole('button', { name: 'Confirm delete app' }).click()
  ]);

  expect(new URL(deleteRequest.url()).pathname).toBe(`/admin/api/apps/${createdApp.appId}`);
  expect(deleteRequest.url()).not.toContain('/admin/events/ws');
  expect(deleteRequest.headers()['x-admin-request']).toBe('true');
  expect(deleteRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(deleteResponse.status()).toBe(200);
  await expectNoWebSocketSends(page, wsSendCount);
  await expect(page.getByTestId('admin-flash')).toContainText('App deleted successfully');
  await expect(page.locator(`[data-app-id="${createdApp.appId}"]`)).toHaveCount(0);
  await expect.poll(() => page.evaluate(() => window.AdminUITest?.getSentMessages?.() || [])).toEqual([]);

  await context.close();
});

test('apps list toggle and delete flows update in place without hard navigation', async ({ browser }) => {
  const context = await newAuthenticatedContext(browser);
  const page = await context.newPage();
  const suffix = `${Date.now()}`;
  const appName = `playwright-toggle-delete-${suffix}`;

  await page.goto(`${baseURL()}/admin/apps#/apps/new`);
  await expect(page.getByRole('heading', { name: 'New App' })).toBeVisible();

  await page.locator('#name').fill(appName);
  await page.locator('#webhook_secret').fill(`playwright-toggle-secret-${suffix}`);
  await page.locator('#service_name').fill(`playwright-toggle-${suffix}.service`);
  await page.locator('#binary_path').fill(`/opt/playwright-toggle-${suffix}`);
  await page.locator('#github_repo').fill('example/playwright-toggle-delete');
  await page.locator('#artifact_name').fill(`playwright-toggle-artifact-${suffix}`);

  const [createRequest, createResponse] = await Promise.all([
    page.waitForRequest((req) => req.url().includes('/admin/api/apps') && req.method() === 'POST'),
    page.waitForResponse((res) => res.url().includes('/admin/api/apps') && res.request().method() === 'POST'),
    page.locator('#submit-btn').click()
  ]);

  expect(createRequest.headers()['x-admin-request']).toBe('true');
  expect(createRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(createResponse.status()).toBe(201);

  await expect(page).toHaveURL(/\/admin\/apps#\//);
  await expect(page.locator('#apps-table')).toBeVisible();

  const appCard = page.locator('[data-app-id]').filter({ hasText: appName }).first();
  await expect(appCard).toBeVisible();

  const appId = await appCard.getAttribute('data-app-id');
  const appsURL = page.url();
  const navigationCount = await page.evaluate(() => performance.getEntriesByType('navigation').length);

  const [disableRequest, disableResponse] = await Promise.all([
    page.waitForRequest((request) => request.url().includes(`/admin/api/apps/${appId}/toggle`) && request.method() === 'POST'),
    page.waitForResponse((response) => response.url().includes(`/admin/api/apps/${appId}/toggle`) && response.request().method() === 'POST'),
    appCard.getByRole('button', { name: 'Disable' }).click()
  ]);

  expect(disableRequest.headers()['x-admin-request']).toBe('true');
  expect(disableRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(disableResponse.status()).toBe(200);
  expect(disableResponse.headers()['x-admin-location']).toBeUndefined();
  await expect(page).toHaveURL(appsURL);
  expect(await page.evaluate(() => performance.getEntriesByType('navigation').length)).toBe(navigationCount);
  await expect(page.getByTestId('admin-flash')).toContainText('App disabled successfully');
  await expect(page.locator(`#app-card-${appId}`)).toContainText('Disabled');
  await expect(page.locator(`#app-card-${appId}`).getByRole('button', { name: 'Enable' })).toBeVisible();

  const [enableRequest, enableResponse] = await Promise.all([
    page.waitForRequest((request) => request.url().includes(`/admin/api/apps/${appId}/toggle`) && request.method() === 'POST'),
    page.waitForResponse((response) => response.url().includes(`/admin/api/apps/${appId}/toggle`) && response.request().method() === 'POST'),
    page.locator(`#app-card-${appId}`).getByRole('button', { name: 'Enable' }).click()
  ]);

  expect(enableRequest.headers()['x-admin-request']).toBe('true');
  expect(enableRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(enableResponse.status()).toBe(200);
  expect(enableResponse.headers()['x-admin-location']).toBeUndefined();
  await expect(page).toHaveURL(appsURL);
  expect(await page.evaluate(() => performance.getEntriesByType('navigation').length)).toBe(navigationCount);
  await expect(page.getByTestId('admin-flash')).toContainText('App enabled successfully');
  await expect(page.locator(`#app-card-${appId}`)).toContainText('Active');
  await expect(page.locator(`#app-card-${appId}`).getByRole('button', { name: 'Disable' })).toBeVisible();

  const appCountBeforeDelete = await page.locator('[data-app-id]').count();
  await page.locator(`#app-card-${appId}`).getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByText('Are you sure you want to delete this app?')).toBeVisible();
  const [deleteRequest, deleteResponse] = await Promise.all([
    page.waitForRequest((request) => request.url().includes(`/admin/api/apps/${appId}`) && request.method() === 'DELETE'),
    page.waitForResponse((response) => response.url().includes(`/admin/api/apps/${appId}`) && response.request().method() === 'DELETE'),
    page.getByRole('button', { name: 'Confirm delete app' }).click()
  ]);

  expect(deleteRequest.headers()['x-admin-request']).toBe('true');
  expect(deleteRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(deleteResponse.status()).toBe(200);
  expect(deleteResponse.headers()['x-admin-location']).toBeUndefined();
  await expect(page).toHaveURL(appsURL);
  expect(await page.evaluate(() => performance.getEntriesByType('navigation').length)).toBe(navigationCount);
  await expect(page.getByTestId('admin-flash')).toContainText('App deleted successfully');
  await expect(page.locator(`#app-card-${appId}`)).toHaveCount(0);
  await expect(page.locator('[data-app-id]')).toHaveCount(appCountBeforeDelete - 1);

  await context.close();
});

test('new app form keeps values on AdminUI validation and navigates on success', async ({ browser }) => {
  const context = await newAuthenticatedContext(browser);
  const page = await context.newPage();

  await page.goto(`${baseURL()}/admin/apps#/apps/new`);

  await expect(page.getByRole('heading', { name: 'New App' })).toBeVisible();

  await page.locator('#name').fill('playwright-inline-app');
  await page.locator('#webhook_secret').fill('bootstrap-secret');
  await page.locator('#service_name').fill('playwright-bootstrap.service');
  await page.locator('#binary_path').fill('/tmp/playwright-bootstrap-binary');
  await page.locator('#github_repo').fill('example/playwright-inline');
  await page.locator('#artifact_name').fill('playwright-inline-artifact');

  const [invalidRequest, invalidResponse] = await Promise.all([
    page.waitForRequest((req) => req.url().includes('/admin/api/apps') && req.method() === 'POST'),
    page.waitForResponse((res) => res.url().includes('/admin/api/apps') && res.request().method() === 'POST'),
    page.locator('#submit-btn').click()
  ]);

  expect(invalidRequest.headers()['x-admin-request']).toBe('true');
  expect(invalidRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(invalidResponse.status()).toBe(400);
  await expect(page.locator('#form-errors')).toBeVisible();
  await expect(page.locator('#name')).toHaveValue('playwright-inline-app');
  await expect(page.locator('#form-errors')).toContainText('An app with this binary path, service name, or webhook secret already exists');

  await page.locator('#webhook_secret').fill('playwright-created-secret');
  await page.locator('#service_name').fill('playwright-inline.service');
  await page.locator('#binary_path').fill('/opt/playwright-inline');
  await page.locator('#github_repo').fill('example/playwright-inline');
  await page.locator('#artifact_name').fill('playwright-inline-artifact');

  const [successRequest, successResponse] = await Promise.all([
    page.waitForRequest((req) => req.url().includes('/admin/api/apps') && req.method() === 'POST'),
    page.waitForResponse((res) => res.url().includes('/admin/api/apps') && res.request().method() === 'POST'),
    page.locator('#submit-btn').click()
  ]);

  expect(successRequest.headers()['x-admin-request']).toBe('true');
  expect(successRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(successResponse.status()).toBe(201);

  await expect(page).toHaveURL(/\/admin\/apps#\//);
  await expect(page.getByTestId('admin-flash')).toContainText('App created successfully');
  await expect(page.locator('#apps-table')).toBeVisible();
  await expect(page.locator('[data-app-id]').filter({ hasText: 'playwright-inline-app' }).first()).toBeVisible();

  await context.close();
});

test('edit app keeps invalid AdminUI submission local with inline errors', async ({ browser }) => {
  const context = await newAuthenticatedContext(browser);
  const page = await context.newPage();

  const conflictApp = await createApp(page, `${Date.now()}-conflict`, 'playwright-edit-conflict');
  const editableApp = await createApp(page, `${Date.now()}-target`, 'playwright-edit-target');

  await page.locator(`[data-app-id="${editableApp.appId}"]`).getByRole('link', { name: 'Edit' }).click();

  await expect(page).toHaveURL(/\/admin\/apps#\/apps\/[^/]+\/edit$/);
  await expect(page.getByRole('heading', { name: 'Edit App' })).toBeVisible();

  const navigationCount = await page.evaluate(() => performance.getEntriesByType('navigation').length);

  await page.locator('#name').fill('playwright-edit-target-invalid');
  await page.locator('#service_name').fill(conflictApp.serviceName);
  await page.locator('#binary_path').fill(conflictApp.binaryPath);

  const [invalidRequest, invalidResponse] = await Promise.all([
    page.waitForRequest((request) => request.url().includes('/admin/api/apps/') && request.method() === 'PUT'),
    page.waitForResponse((response) => response.url().includes('/admin/api/apps/') && response.request().method() === 'PUT'),
    page.locator('#submit-btn').click()
  ]);

  expect(invalidRequest.headers()['x-admin-request']).toBe('true');
  expect(invalidRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(invalidResponse.status()).toBe(400);
  await expect(page).toHaveURL(/\/admin\/apps#\/apps\/[^/]+\/edit$/);
  expect(await page.evaluate(() => performance.getEntriesByType('navigation').length)).toBe(navigationCount);
  await expect(page.getByRole('heading', { name: 'Edit App' })).toBeVisible();
  await expect(page.locator('#app-form')).toBeVisible();
  await expect(page.locator('#form-errors')).toBeVisible();
  await expect(page.locator('#form-errors')).toContainText('An app with this binary path, service name, or webhook secret already exists');
  await expect(page.locator('#name')).toHaveValue('playwright-edit-target-invalid');
  await expect(page.locator('#service_name')).toHaveValue(conflictApp.serviceName);
  await expect(page.locator('#binary_path')).toHaveValue(conflictApp.binaryPath);

  await context.close();
});

test('edit app via AdminUI navigation shows success flash after native redirect', async ({ browser }) => {
  const context = await newAuthenticatedContext(browser);
  const page = await context.newPage();

  const editableApp = await createApp(page, `${Date.now()}-success`, 'playwright-edit-success');

  await page.locator(`[data-app-id="${editableApp.appId}"]`).getByRole('link', { name: 'Edit' }).click();

  await expect(page).toHaveURL(/\/admin\/apps#\/apps\/[^/]+\/edit$/);
  await expect(page.getByRole('heading', { name: 'Edit App' })).toBeVisible();

  await page.locator('#name').fill('playwright-edit-success-updated');

  const [updateRequest, updateResponse] = await Promise.all([
    page.waitForRequest((request) => request.url().includes('/admin/api/apps/') && request.method() === 'PUT'),
    page.waitForResponse((response) => response.url().includes('/admin/api/apps/') && response.request().method() === 'PUT'),
    page.locator('#submit-btn').click()
  ]);

  expect(updateRequest.headers()['x-admin-request']).toBe('true');
  expect(updateRequest.headers()['x-requested-with']).toBe('AdminUI');
  expect(updateResponse.status()).toBe(200);

  await expect(page).toHaveURL(/\/admin\/apps#\//);
  await expect(page.getByTestId('admin-flash')).toContainText('App updated successfully');
  await expect(page.locator('#apps-table')).toBeVisible();
  await expect(page.locator(`[data-app-id="${editableApp.appId}"]`)).toContainText('playwright-edit-success-updated');

  await context.close();
});
