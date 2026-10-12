// 同源静态前端→真实 Go→专用 PG；仅销毁首个已返回的 WARP 响应，不模拟业务 API。
// 运行配置只含本次临时二进制/data/密钥路径；随机密码与 JWT 只在进程/浏览器内存使用。
import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { createReadStream, createWriteStream, existsSync, readFileSync, writeFileSync } from "node:fs";
import http from "node:http";
import { dirname, extname, resolve, sep } from "node:path";

const root = resolve(import.meta.dirname, "..");
const output = resolve(root, "output/verification/idempotency");
const config = JSON.parse(readFileSync(resolve(output, "real-environment.json"), "utf8").replace(/^\uFEFF/, ""));
const backend = spawn(config.binary, ["-addr", "127.0.0.1:18787", "-data", config.data, "-proxy", ""], {
  cwd: dirname(config.binary), windowsHide: true,
  env: { ...process.env, ARTEX_PG_DSN: `postgres://artex_test@127.0.0.1:25432/${config.database}?sslmode=disable`, ARTEX_PROXY_SECRETS_FILE: config.secrets, Path: dirname(config.binary), PATH: dirname(config.binary) },
  stdio: ["ignore", "pipe", "pipe"],
});
const backendLog = createWriteStream(resolve(output, "real-backend.log"));
backend.stdout.pipe(backendLog);
backend.stderr.pipe(backendLog);
const api = async (path, payload) => {
  const result = await fetch(`http://127.0.0.1:18787/api${path}`, { method: payload ? "POST" : "GET", headers: { "Content-Type": "application/json" }, body: payload ? JSON.stringify(payload) : undefined });
  if (!result.ok) throw new Error(`HTTP ${result.status}`);
  return result.json();
};
let ready = false;
for (let attempt = 0; attempt < 100; attempt++) {
  try { await api("/health"); ready = true; break; } catch { await new Promise((resolve) => setTimeout(resolve, 200)); }
}
if (!ready) { backend.kill(); throw new Error("真实 Go 后端未就绪"); }
let token;
try { token = (await api("/auth/init", { password: `Synthetic-${randomBytes(24).toString("hex")}!` })).token; }
catch { backend.kill(); throw new Error("新库初始化失败；未尝试重置任何已有账户"); }
if (!token) { backend.kill(); throw new Error("初始化未返回 token"); }
const records = [];
let dropNext = true;
const persist = () => writeFileSync(resolve(output, "real-go-http-evidence.json"), JSON.stringify({ backend: "真实生产 Go 二进制", database: config.database, warp_tools: "PATH 隔离，不能执行 sh/wgcf/wireproxy", records }, null, 2));
const json = (response, status, data) => { response.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" }); response.end(JSON.stringify(data)); };
const frontend = http.createServer(async (request, response) => {
  try {
    const url = new URL(request.url, "http://127.0.0.1:4173");
    if (url.pathname === "/__real/bootstrap") {
      response.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store" });
      response.end(`<script>localStorage.setItem('artex_token',${JSON.stringify(token)});document.cookie='artex_token='+${JSON.stringify(token)}+'; path=/';location.href='/system/proxies/'</script>`);
      return;
    }
    if (url.pathname === "/__real/evidence") return json(response, 200, { database: config.database, records });
    if (url.pathname === "/__real/stop" && request.method === "POST") { json(response, 200, { stopped: true }); setTimeout(stop, 100); return; }
    if (url.pathname.startsWith("/api/")) {
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const raw = Buffer.concat(chunks);
      const key = request.headers["idempotency-key"];
      const headers = { "Content-Type": "application/json", Authorization: `Bearer ${token}`, ...(key ? { "Idempotency-Key": key } : {}) };
      const result = await fetch(`http://127.0.0.1:18787${url.pathname}${url.search}`, { method: request.method, headers, body: raw.length ? raw : undefined });
      const text = await result.text();
      if (url.pathname === "/api/proxies" && request.method === "POST") {
        const payload = JSON.parse(raw.toString());
        const dto = JSON.parse(text);
        const dropped = dropNext && payload.kind === "warp";
        records.push({ method: "POST", path: url.pathname, key, forwarded_key: headers["Idempotency-Key"], payload, status: result.status, proxy_id: dto.proxy_id ?? dto.id, operation_status: dto.operation_status, warp_phase: dto.proxy?.warp_phase ?? dto.warp_phase, response_dropped_after_backend_completed: dropped });
        persist();
        if (dropped) { dropNext = false; response.destroy(); return; }
      }
      response.writeHead(result.status, { "Content-Type": result.headers.get("content-type") ?? "application/json" });
      response.end(text); return;
    }
    const staticRoot = resolve(root, "out");
    const file = resolve(staticRoot, `.${decodeURIComponent(url.pathname)}`, url.pathname.endsWith("/") ? "index.html" : "");
    if (!file.startsWith(staticRoot + sep) || !existsSync(file)) return json(response, 404, { error: "not found" });
    const types = { ".html": "text/html; charset=utf-8", ".js": "application/javascript", ".css": "text/css", ".woff2": "font/woff2", ".png": "image/png", ".txt": "text/plain" };
    response.writeHead(200, { "Content-Type": types[extname(file)] ?? "application/octet-stream" });
    createReadStream(file).pipe(response);
  } catch { json(response, 502, { error: "本地同源转发失败" }); }
});
frontend.listen(4173, "127.0.0.1", () => {
  writeFileSync(resolve(output, "real-processes.json"), JSON.stringify({ frontend_pid: process.pid, backend_pid: backend.pid, ports: [4173, 18787], database: config.database }, null, 2));
  console.log("真实 Go/PG 本地环境就绪：http://127.0.0.1:4173/__real/bootstrap；首个 WARP 完整返回后销毁响应");
});
function stop() { persist(); frontend.close(); backend.kill(); backendLog.end(); setTimeout(() => process.exit(0), 300); }
process.on("SIGINT", stop);
process.on("SIGTERM", stop);
