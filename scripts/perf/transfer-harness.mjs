// 002 harness: what a cold load of each route downloads (JS/RSC/CSS/fonts), including the
// <Link> viewport prefetches that fire after load, plus FCP/LCP, and a sidebar navigation
// "/" -> "/apps/" -> "/history/" transfer. Usage:
//   node scripts/perf/transfer-harness.mjs <binary> <out.json>
// The server runs with a DB under $PERF_RUN_DIR/run-transfer-<ts>/ (default PERF_RUN_DIR =
// reference/executor, ignored by git: the run dir holds the generated initial-admin-password).
// Moved from reference/spike/transfer_harness.mjs (T002); only the run dir changed (RV-19).
import { spawn } from "node:child_process";
import fs from "node:fs";
import http from "node:http";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";

const [binArg, outPath] = process.argv.slice(2);
const bin = path.resolve(binArg); // the server runs with cwd = runDir: a relative path would not resolve
const ROUTES = ["/", "/apps/", "/apps/new/", "/apps/edit/?id=x", "/history/", "/settings/github/", "/settings/telegram/", "/logs/", "/account/", "/login/"];
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const runParent = process.env.PERF_RUN_DIR ? path.resolve(process.env.PERF_RUN_DIR) : path.join(repoRoot, "reference", "executor");
const runDir = path.join(runParent, `run-transfer-${Date.now()}`);
fs.mkdirSync(runDir, { recursive: true });
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
const srv = spawn(bin, [], { env, cwd: runDir, stdio: "ignore" });
process.on("exit", () => { try { srv.kill("SIGTERM"); } catch {} });
for (let i = 0; i < 120; i++) { try { const r = await req("GET", `${base}/admin/api/v1/session`); if (r.status === 401) break; } catch {} await sleep(250); }
const bh = { "X-Admin-Request": "true", "Sec-Fetch-Site": "same-origin", Origin: base };
const initial = fs.readFileSync(path.join(runDir, "initial-admin-password"), "utf8").trim();
const c1 = cookieOf(await req("POST", `${base}/admin/api/v1/session`, bh, { username: "admin", password: initial }));
const ck = cookieOf(await req("POST", `${base}/admin/api/v1/account/password`, { ...bh, Cookie: `${c1.name}=${c1.value}` }, { currentPassword: initial, newPassword: "harness-secret-123" }));

const browser = await chromium.launch();
async function ctxWithCookie() {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  await ctx.addCookies([{ name: ck.name, value: ck.value, domain: "127.0.0.1", path: "/", httpOnly: true, secure: false, sameSite: "Strict" }]);
  await ctx.addInitScript(() => { localStorage.setItem("ad.locale", "id"); localStorage.setItem("theme", "light"); });
  return ctx;
}
function track(page) {
  const t = { byKind: {}, phase: "load", list: [] };
  page.on("requestfinished", async (r) => {
    const u = new URL(r.url());
    if (!u.pathname.startsWith("/admin/")) return;
    let kind = u.pathname.startsWith("/admin/api/") ? "api" : u.pathname.endsWith(".js") ? "js" : u.pathname.endsWith(".txt") || u.searchParams.has("_rsc") ? "rsc" : u.pathname.endsWith(".css") ? "css" : u.pathname.endsWith(".woff2") ? "font" : "other";
    const sizes = await r.sizes().catch(() => ({ responseBodySize: 0, responseHeadersSize: 0 }));
    const key = `${t.phase}:${kind}`;
    t.byKind[key] ??= { n: 0, bytes: 0 };
    t.byKind[key].n++; t.byKind[key].bytes += sizes.responseBodySize;
    t.list.push(`${t.phase} ${kind} ${u.pathname}${u.search} ${sizes.responseBodySize}`);
  });
  return t;
}
const results = { routes: {}, navigation: {} };
for (const route of ROUTES) {
  const ctx = await ctxWithCookie();
  if (route === "/login/") await ctx.clearCookies();
  const page = await ctx.newPage();
  const t = track(page);
  await page.goto(`${base}/admin${route}`, { waitUntil: "load" });
  t.phase = "afterload";
  await page.waitForLoadState("networkidle").catch(() => {});
  await sleep(4000); // viewport prefetches of sidebar links fire after hydration / idle
  const perf = await page.evaluate(() => new Promise((resolve) => {
    const fcp = performance.getEntriesByName("first-contentful-paint")[0]?.startTime ?? -1;
    let lcp = -1;
    try { new PerformanceObserver((l) => { for (const e of l.getEntries()) lcp = e.startTime; }).observe({ type: "largest-contentful-paint", buffered: true }); } catch {}
    let tbt = 0;
    try { new PerformanceObserver((l) => { for (const e of l.getEntries()) tbt += Math.max(0, e.duration - 50); }).observe({ type: "longtask", buffered: true }); } catch {}
    setTimeout(() => resolve({ fcp: Math.round(fcp), lcp: Math.round(lcp), tbtApprox: Math.round(tbt) }), 300);
  }));
  results.routes[route] = { ...t.byKind, perf, list: t.list };
  await ctx.close();
}
{
  const ctx = await ctxWithCookie();
  const page = await ctx.newPage();
  const t = track(page);
  await page.goto(`${base}/admin/`, { waitUntil: "load" });
  await page.waitForLoadState("networkidle").catch(() => {});
  await sleep(4000);
  for (const [label, href] of [["apps", "/admin/apps/"], ["history", "/admin/history/"], ["logs", "/admin/logs/"]]) {
    t.phase = `nav-${label}`;
    await page.locator(`a[href="${href}"]`).first().click();
    await page.waitForURL(`**${href}`);
    await page.waitForLoadState("networkidle").catch(() => {});
    await sleep(1500);
  }
  results.navigation = { ...t.byKind, list: t.list };
  await ctx.close();
}
await browser.close();
fs.writeFileSync(outPath, JSON.stringify(results, null, 1));
const summary = {};
for (const [r, v] of Object.entries(results.routes)) { summary[r] = Object.fromEntries(Object.entries(v).filter(([k]) => k !== "list")); }
summary.navigation = Object.fromEntries(Object.entries(results.navigation).filter(([k]) => k !== "list"));
console.log(JSON.stringify(summary, null, 1));
process.exit(0);
