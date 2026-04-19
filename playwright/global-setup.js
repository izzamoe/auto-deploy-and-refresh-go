const { spawn } = require('node:child_process');
const fs = require('node:fs');
const fsp = require('node:fs/promises');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');

async function getFreePort() {
  return await new Promise((resolve, reject) => {
    const server = net.createServer();
    server.unref();
    server.on('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      const port = typeof address === 'object' && address ? address.port : null;
      server.close(() => {
        if (!port) {
          reject(new Error('failed to allocate a free port'));
          return;
        }
        resolve(port);
      });
    });
  });
}

async function waitForAdmin(baseURL, timeoutMs = 30000) {
  const started = Date.now();
  while (Date.now() - started < timeoutMs) {
    try {
      const response = await fetch(`${baseURL}/admin/apps`, {
        redirect: 'manual'
      });
      if (response.status === 401) {
        return;
      }
    } catch {
      // keep retrying until the server is up
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }

  throw new Error(`admin UI did not become ready at ${baseURL}`);
}

module.exports = async () => {
  const rootDir = path.resolve(__dirname, '..');
  const tempDir = await fsp.mkdtemp(path.join(os.tmpdir(), 'auto-deploy-playwright-'));
  const binPath = path.join(tempDir, 'auto-deploy-playwright-bin');
  const dbPath = path.join(tempDir, 'playwright.db');
  const port = await getFreePort();
  const baseURL = `http://127.0.0.1:${port}`;

  await new Promise((resolve, reject) => {
    const build = spawn('go', ['build', '-o', binPath, '.'], {
      cwd: rootDir,
      stdio: 'inherit'
    });
    build.on('error', reject);
    build.on('exit', (code) => {
      if (code === 0) {
        resolve();
      } else {
        reject(new Error(`go build failed with exit code ${code}`));
      }
    });
  });

  const env = {
    ...process.env,
    LISTEN_ADDR: `127.0.0.1:${port}`,
    DEPLOY_QUEUE_DB_PATH: dbPath,
    ADMIN_USERNAME: 'playwright',
    ADMIN_PASSWORD: 'playwright-secret',
    WEBHOOK_SECRET: 'bootstrap-secret',
    DEPLOY_BINARY_PATH: '/tmp/playwright-bootstrap-binary',
    DEPLOY_SERVICE_NAME: 'playwright-bootstrap.service',
    GITHUB_REPO: 'example/playwright',
    ARTIFACT_NAME: 'playwright-artifact'
  };

  const logPath = path.join(tempDir, 'server.log');
  const logStream = fs.createWriteStream(logPath, { flags: 'a' });
  const server = spawn(binPath, [], {
    cwd: rootDir,
    env,
    stdio: ['ignore', 'pipe', 'pipe']
  });
  let exited = false;
  let exitCode = null;
  let exitSignal = null;
  let startupFailure = null;

  const exitPromise = new Promise((resolve) => {
    server.once('exit', (code, signal) => {
      exited = true;
      exitCode = code;
      exitSignal = signal;
      resolve({ code, signal });
    });
  });

  server.stdout.pipe(logStream);
  server.stderr.pipe(logStream);

  const stopServer = async () => {
    if (!exited) {
      server.kill('SIGTERM');
    }

    if (!exited) {
      await Promise.race([
        exitPromise,
        new Promise((resolve) => {
          setTimeout(() => {
            if (!exited) {
              server.kill('SIGKILL');
            }
            resolve();
          }, 5000);
        })
      ]);
    }

    if (!exited) {
      await exitPromise;
    }

    logStream.end();
    await fsp.rm(tempDir, { recursive: true, force: true });
  };

  server.on('error', (err) => {
    startupFailure = err;
  });

  const readyPromise = waitForAdmin(baseURL);
  const startupOutcome = await Promise.race([
    readyPromise.then(() => 'ready'),
    exitPromise.then(() => 'exited')
  ]);

  if (startupFailure) {
    await stopServer().catch(() => {});
    throw startupFailure;
  }

  if (startupOutcome === 'exited') {
    await stopServer().catch(() => {});
    throw new Error(`Go server exited during startup (code=${exitCode}, signal=${exitSignal ?? 'none'})`);
  }

  await readyPromise;

  process.env.PLAYWRIGHT_BASE_URL = baseURL;
  process.env.PLAYWRIGHT_ADMIN_USERNAME = env.ADMIN_USERNAME;
  process.env.PLAYWRIGHT_ADMIN_PASSWORD = env.ADMIN_PASSWORD;
  process.env.PLAYWRIGHT_WEBHOOK_SECRET = env.WEBHOOK_SECRET;

  return async () => {
    await stopServer();
  };
};
