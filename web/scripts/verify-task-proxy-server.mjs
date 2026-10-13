// 任务代理编辑器的合成 HTTP 验收后端，只用于浏览器行为；不能当作 Go/PG 证据。
import { createReadStream, existsSync, mkdirSync, writeFileSync } from "node:fs";
import http from "node:http";
import { extname, resolve, sep } from "node:path";
const root = resolve(import.meta.dirname, "..");
const output = resolve(root, "output/verification/task-proxy-switch");
mkdirSync(output, { recursive: true });
const items = [
  { id: 1, name: "原全局快照", kind: "http", enabled: true, status: "healthy", egress_ip: "203.0.113.1" },
  { id: 2, name: "新全局 HTTP", kind: "http", enabled: true, status: "healthy", egress_ip: "203.0.113.2" },
  { id: 3, name: "未检测代理", kind: "http", enabled: true, status: "unknown" },
  { id: 4, name: "搜索 WARP 出口", kind: "warp", enabled: true, status: "healthy", warp_phase: "ready", egress_ip: "203.0.113.4" },
  { id: 5, name: "已停用代理", kind: "socks5", enabled: false, status: "healthy", egress_ip: "198.51.100.5" },
];
let globalID = 2;
let task = { id: "12", name: "合成任务代理切换", description: "仅验证前端 UI 与请求，非 Go/PG", goal: "验证切换快照", status: "paused", paused: true, in_flight: 0, created_at: new Date().toISOString(), proxy_mode: "inherit", proxy_revision: 0, proxy_id: 1, proxy: { ...items[0] } };
let behavior = {};
const records = [];
const persist = () => writeFileSync(resolve(output, "synthetic-http-evidence.json"), JSON.stringify({ synthetic: true, task, globalID, records }, null, 2));
const json = (res, status, data) => { res.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" }); res.end(JSON.stringify(data)); };
const body = async (req) => { const chunks = []; for await (const chunk of req) chunks.push(chunk); return chunks.length ? JSON.parse(Buffer.concat(chunks)) : {}; };
const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, "http://127.0.0.1:4173");
  if (url.pathname === "/__fixture/bootstrap") {
    res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
    res.end("<script>localStorage.setItem('artex_token','synthetic-only');document.cookie='artex_token=synthetic-only; path=/';location.href='/function/tasks/detail/?id=12'</script>"); return;
  }
  if (url.pathname === "/__fixture/evidence") return json(res, 200, { synthetic: true, task, records });
  if (url.pathname === "/__fixture/control") {
    const data = await body(req);
    behavior = data.behavior ?? {};
    if (data.task) task = { ...task, ...data.task };
    if ("globalID" in data) globalID = data.globalID;
    persist(); return json(res, 200, { ok: true });
  }
  if (url.pathname === "/__fixture/stop") { json(res, 200, { stopped: true }); server.close(); return; }
  if (url.pathname === "/api/proxies") {
    if (behavior.directoryFailure) return json(res, 503, { error: "合成目录加载失败" });
    return json(res, 200, { proxies: items, global_proxy_id: globalID });
  }
  if (url.pathname === "/api/tasks/12/proxy" && req.method === "PUT") {
    const input = await body(req);
    const record = { method: "PUT", path: url.pathname, body: input };
    records.push(record);
    if (behavior.delay) await new Promise((resolve) => setTimeout(resolve, behavior.delay));
    if (!Number.isSafeInteger(input.expected_revision) || input.expected_revision < 0) { record.status = 400; persist(); return json(res, 400, { error: "expected_revision无效" }); }
    if (input.expected_revision !== task.proxy_revision) { record.status = 409; persist(); return json(res, 409, { error: "任务代理版本已更新", code: "proxy_revision_conflict", current_revision: task.proxy_revision }); }
    if (behavior.saveFailure) { record.status = behavior.saveFailure; persist(); return json(res, record.status, { error: "合成：仍有真实工具在途", code: "proxy_busy" }); }
    const id = input.proxy_mode === "inherit" ? globalID : input.proxy_mode === "fixed" ? input.proxy_id : null;
    const proxy = items.find((item) => item.id === id);
    if (id !== null && (!proxy?.enabled || proxy?.status !== "healthy")) { record.status = 400; persist(); return json(res, 400, { error: "代理不可用" }); }
    task = { ...task, proxy_mode: input.proxy_mode, proxy_revision: task.proxy_revision + 1, proxy_id: id ?? undefined, proxy: proxy ? { ...proxy } : undefined };
    record.status = 200;
    if (behavior.failReadback) behavior.readFailures = 1;
    persist(); return json(res, 200, task);
  }
  if (url.pathname === "/api/tasks/12") {
    if (behavior.readFailures > 0) { behavior.readFailures--; records.push({ method: "GET", path: url.pathname, status: 503 }); persist(); return json(res, 503, { error: "合成回读失败" }); }
    records.push({ method: "GET", path: url.pathname, status: 200, mode: task.proxy_mode, id: task.proxy_id, revision: task.proxy_revision });
    persist(); return json(res, 200, task);
  }
  if (url.pathname === "/api/auth/status") return json(res, 200, { initialized: true, authenticated: true });
  if (url.pathname === "/api/llm/profiles") return json(res, 200, { profiles: [] });
  if (url.pathname === "/api/stats") return json(res, 200, {});
  if (url.pathname.includes("/events") || url.pathname.includes("/stream")) { res.writeHead(200, { "Content-Type": "text/event-stream" }); res.end(); return; }
  if (url.pathname.includes("/main-sessions")) return json(res, 200, { sessions: [] });
  if (url.pathname.startsWith("/api/")) return json(res, 200, { tasks: [], rows: [], records: [], items: [], activity: [], intents: [], sessions: [], findings: [], approvals: [] });
  const staticRoot = resolve(root, "out");
  const file = resolve(staticRoot, `.${decodeURIComponent(url.pathname)}`, url.pathname.endsWith("/") ? "index.html" : "");
  if (!file.startsWith(staticRoot + sep) || !existsSync(file)) return json(res, 404, { error: "not found" });
  const types = { ".html": "text/html; charset=utf-8", ".js": "application/javascript", ".css": "text/css", ".png": "image/png", ".woff2": "font/woff2", ".txt": "text/plain" };
  res.writeHead(200, { "Content-Type": types[extname(file)] ?? "application/octet-stream" }); createReadStream(file).pipe(res);
});
server.listen(4173, "127.0.0.1", () => console.log("合成任务代理验收服务：http://127.0.0.1:4173/__fixture/bootstrap"));
