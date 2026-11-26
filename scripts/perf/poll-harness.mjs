// 002 harness: REST requests per minute per idle tab, per route, with N apps.
// Usage: node scripts/perf/poll-harness.mjs <binary> <N apps> <window seconds> <out.json>
// Starts the binary on a free loopback port with a DB under $PERF_RUN_DIR/run-<N>-<ts>/
// (default PERF_RUN_DIR = reference/executor, ignored by git: the run dir holds the
// generated initial-admin-password), signs in (initial password -> new password), creates N
// apps through the API, opens every route in its own browser context, waits for the first
// render + networkidle, then counts /admin/api/v1/* requests for <window> seconds (idle, no
// interaction). Afterwards per route:
//   focus:     dispatch one visibilitychange (visible) after staleTime and count requests in 3 s
//   reconnect: context offline 5 s -> online, count requests in 5 s (WS reconnect + onlineManager)
// Also counts WS frames received during the idle window.
// Moved from reference/spike/poll_harness.mjs (T002); only the run dir (PERF_RUN_DIR) and the
// `base` field (git rev-parse --short HEAD) changed (RV-19).
import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import http from "node:http";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";

const [bin, nArg, winArg, outPath] = process.argv.slice(2);
const N = Number(nArg);
const WINDOW_S = Number(winArg);
const ROUTES = ["/", "/apps/", "/apps/new/", "/history/", "/settings/github/", "/settings/telegram/", "/logs/", "/account/"];
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const runParent = process.env.PERF_RUN_DIR ? path.resolve(process.env.PERF_RUN_DIR) : path.join(repoRoot, "reference", "executor");
const runDir = path.join(runParent, `run-${N}-${Date.now()}`);
fs.mkdirSync(runDir, { recursive: true });
let baseRev = "unknown";
try { baseRev = execFileSync("git", ["rev-parse", "--short", "HEAD"], { cwd: repoRoot }).toString().trim(); } catch {}

const freePort = () => new Promise((res) => { const s = net.createServer(); s.listen(0, "127.0.0.1", () => { const p = s.address().port; s.close(() => res(p)); }); });
function req(method, url, headers = {}, body) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const payload = body === undefined ? null : Buffer.from(JSON.stringify(body));
    const r = http.request({ method, hostname: u.hostname, port: u.port, path: u.pathname + u.search, headers: { ...(payload ? { "Content-Type": "application/json", "Content-Length": payload.length } : {}), ...headers } }, (res) => {
      const chunks = []; res.on("data", (c) => chunks.push(c)); res.on("end", () => resolve({ status: res.statusCode, headers: res.headers, body: Buffer.concat(chunks).toString() }));
    });
    r.on("error", reject); if (payload) r.write(payload); r.end();
  });
}
const cookieOf = (res) => { for (const l of [].concat(res.headers["set-cookie"] || [])) { const [pair] = l.split(";"); const i = pair.indexOf("="); const n = pair.slice(0, i); if (n.endsWith("admin_session")) return { name: n, value: pair.slice(i + 1) }; } return null; };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const port = await freePort();
const base = `http://127.0.0.1:${port}`;
const env = { ...process.env, LISTEN_ADDR: `127.0.0.1:${port}`, DEPLOY_QUEUE_DB_PATH: path.join(runDir, "db.sqlite"), COOKIE_SECURE: "false", WEBHOOK_SECRET: "bootstrap-secret-bootstrap-secret-xx", DEPLOY_BINARY_PATH: path.join(runDir, "boot-bin"), DEPLOY_SERVICE_NAME: "pw-boot.service", GITHUB_REPO: "example/boot", ARTIFACT_NAME: "boot" };
delete env.ADMIN_USERNAME; delete env.ADMIN_PASSWORD;
const log = fs.createWriteStream(path.join(runDir, "server.log"));
const srv = spawn(bin, [], { env, cwd: runDir, stdio: ["ignore", "pipe", "pipe"] });
srv.stdout.pipe(log); srv.stderr.pipe(log);
const stop = () => { try { srv.kill("SIGTERM"); } catch {} };
process.on("exit", stop);

for (let i = 0; i < 120; i++) { try { const r = await req("GET", `${base}/admin/api/v1/session`); if (r.status === 401) break; } catch {} await sleep(250); }
const bh = { "X-Admin-Request": "true", "Sec-Fetch-Site": "same-origin", Origin: base };
const initial = fs.readFileSync(path.join(runDir, "initial-admin-password"), "utf8").trim();
const login = await req("POST", `${base}/admin/api/v1/session`, bh, { username: "admin", password: initial });
const c1 = cookieOf(login);
const ch = await req("POST", `${base}/admin/api/v1/account/password`, { ...bh, Cookie: `${c1.name}=${c1.value}` }, { currentPassword: initial, newPassword: "harness-secret-123" });
const ck = cookieOf(ch);
if (!ck) throw new Error(`password change failed ${ch.status} ${ch.body}`);
const auth = { ...bh, Cookie: `${ck.name}=${ck.value}` };
const existing = JSON.parse((await req("GET", `${base}/admin/api/v1/apps`, auth)).body).apps.length;
for (let i = existing; i < N; i++) {
  const r = await req("POST", `${base}/admin/api/v1/apps`, auth, { name: `app-${i}`, binaryPath: `/opt/harness/app-${i}/app`, serviceName: `harness-app-${i}.service`, githubRepo: `example/app-${i}`, artifactName: `app-${i}` });
  if (r.status !== 201 && r.status !== 200) throw new Error(`create app ${i}: ${r.status} ${r.body}`);
}
const appCount = JSON.parse((await req("GET", `${base}/admin/api/v1/apps`, auth)).body).apps.length;

const browser = await chromium.launch();
const results = {};
async function openRoute(route) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  await ctx.addCookies([{ name: ck.name, value: ck.value, domain: "127.0.0.1", path: "/", httpOnly: true, secure: false, sameSite: "Strict" }]);
  await ctx.addInitScript(() => { localStorage.setItem("ad.locale", "id"); localStorage.setItem("theme", "light"); });
  const page = await ctx.newPage();
  const st = { route, initial: [], idle: [], between: [], focus: [], reconnect: [], wsFrames: 0, wsOpened: 0, phase: "initial" };
  page.on("request", (r) => { const u = new URL(r.url()); if (u.pathname.startsWith("/admin/api/v1/")) st[st.phase]?.push(`${r.method()} ${u.pathname}${u.search}`); });
  page.on("websocket", (ws) => { st.wsOpened++; ws.on("framereceived", () => { if (st.phase === "idle") st.wsFrames++; }); });
  await page.goto(`${base}/admin${route}`, { waitUntil: "load" });
  await page.locator("main h1, main h2").first().waitFor({ state: "attached", timeout: 30000 }).catch(() => {});
  await page.waitForLoadState("networkidle").catch(() => {});
  await sleep(1500);
  return { ctx, page, st };
}
const opened = [];
for (const r of ROUTES) opened.push(await openRoute(r));
for (const o of opened) o.st.phase = "idle";
const t0 = Date.now();
await sleep(WINDOW_S * 1000);
const idleSec = (Date.now() - t0) / 1000;
for (const o of opened) o.st.phase = "between"; // freeze the idle window for every tab at once
for (const o of opened) {
  o.st.phase = "between";
  await sleep(6000); // > staleTime 5 s, so every query is stale before the focus event
  o.st.phase = "focus";
  // TanStack Query v5 focusManager listens to `visibilitychange` on window.
  await o.page.evaluate(() => { window.dispatchEvent(new Event("visibilitychange")); });
  await sleep(3000);
  o.st.phase = "reconnect";
  await o.ctx.setOffline(true);
  await sleep(5000);
  await o.ctx.setOffline(false);
  await sleep(5000);
  o.st.phase = "done";
}
for (const o of opened) {
  const s = o.st;
  const count = (arr) => arr.reduce((m, k) => { const key = k.replace(/\/apps\/[^/]+\/service\/status/, "/apps/:id/service/status"); m[key] = (m[key] || 0) + 1; return m; }, {});
  results[s.route] = { initial: s.initial.length, idle: s.idle.length, idlePerMin: +(s.idle.length * 60 / idleSec).toFixed(2), idleBreakdown: count(s.idle), focus: s.focus.length, focusBreakdown: count(s.focus), reconnect: s.reconnect.length, reconnectBreakdown: count(s.reconnect), wsOpened: s.wsOpened, wsFramesIdle: s.wsFrames, initialBreakdown: count(s.initial) };
  await o.ctx.close();
}
await browser.close();
stop();
const out = { n: appCount, windowSeconds: idleSec, measuredAt: new Date().toISOString(), base: baseRev, results };
fs.writeFileSync(outPath, JSON.stringify(out, null, 1));
console.log(JSON.stringify(out, null, 1));
process.exit(0);
