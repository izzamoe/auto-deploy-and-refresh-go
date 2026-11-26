// 002 harness: server cost per idle WebSocket connection (RSS, threads, CPU, goroutines) for
// 0/1/10/50 sockets on the event endpoint.
// Usage: node scripts/perf/ws-load.mjs <binary> <out.json> [protocol]
//   protocol "2" (default): sockets only listen (protocol 2 has no client ops).
//   protocol "3": after the server's `hello` every socket sends
//     {"op":"sub","ref":"r1","topics":[{"name":"summary"}]}.
// When PPROF_ADDR is set (loopback host:port) the server inherits it and every step also reads
// GET http://$PPROF_ADDR/debug/pprof/goroutine?debug=1 and records the goroutine total.
// The server runs with a DB under $PERF_RUN_DIR/run-ws-<ts>/ (default PERF_RUN_DIR =
// reference/executor, ignored by git: the run dir holds the generated initial-admin-password).
// Moved from reference/spike/ws_load.mjs (T002): protocol 3 `sub`, PPROF goroutines and run dir
// are the only changes (T002 step 1a, RV-19).
import { spawn } from "node:child_process";
import fs from "node:fs";
import http from "node:http";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";

const [bin, outPath, proto = "2"] = process.argv.slice(2);
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const runParent = process.env.PERF_RUN_DIR ? path.resolve(process.env.PERF_RUN_DIR) : path.join(repoRoot, "reference", "executor");
const runDir = path.join(runParent, `run-ws-${Date.now()}`);
fs.mkdirSync(runDir, { recursive: true });
const pprofAddr = process.env.PPROF_ADDR || "";
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
const pid = srv.pid;
const clk = 100; // USER_HZ
function sample() {
  const st = fs.readFileSync(`/proc/${pid}/status`, "utf8");
  const rss = Number(/VmRSS:\s+(\d+)/.exec(st)[1]);
  const threads = Number(/Threads:\s+(\d+)/.exec(st)[1]);
  const f = fs.readFileSync(`/proc/${pid}/stat`, "utf8").split(") ")[1].split(" ");
  const ticks = Number(f[11]) + Number(f[12]);
  return { rssKiB: rss, threads, cpuTicks: ticks };
}
// goroutines reads the total of the pprof goroutine profile (first line
// "goroutine profile: total N"); null when PPROF_ADDR is not set or the read fails.
async function goroutines() {
  if (!pprofAddr) return null;
  try {
    const r = await req("GET", `http://${pprofAddr}/debug/pprof/goroutine?debug=1`);
    const m = /goroutine profile: total (\d+)/.exec(r.body);
    return m ? Number(m[1]) : null;
  } catch { return null; }
}
const sockets = [];
let frames = 0;
async function openN(n) {
  while (sockets.length < n) {
    const ws = new WebSocket(`ws://127.0.0.1:${port}/admin/events/v1/ws?protocol=${proto}`, { headers: { Cookie: `${ck.name}=${ck.value}`, Origin: base } });
    let subscribed = false;
    ws.onmessage = (ev) => {
      frames++;
      if (proto === "3" && !subscribed) {
        try {
          const msg = JSON.parse(typeof ev.data === "string" ? ev.data : Buffer.from(ev.data).toString());
          if (msg.type === "hello") {
            subscribed = true;
            ws.send(JSON.stringify({ op: "sub", ref: "r1", topics: [{ name: "summary" }] }));
          }
        } catch {}
      }
    };
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
    sockets.push(ws);
  }
}
const out = { protocol: proto, pprofAddr: pprofAddr || null, steps: [] };
for (const n of [0, 1, 10, 50]) {
  await openN(n);
  await sleep(3000);
  const a = sample(); const f0 = frames;
  await sleep(30000);
  const b = sample();
  const g = await goroutines();
  out.steps.push({ connections: n, rssKiB: b.rssKiB, threads: b.threads, goroutines: g, cpuMsPer30s: (b.cpuTicks - a.cpuTicks) * (1000 / clk), framesPer30s: frames - f0 });
  console.log(JSON.stringify(out.steps.at(-1)));
}
for (const s of sockets) s.close();
fs.writeFileSync(outPath, JSON.stringify(out, null, 1));
process.exit(0);
