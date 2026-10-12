// 前端浏览器回归用合成 HTTP 后端；SQLite 只保存测试载荷，不使用真实 WARP 或生产凭据。
import { createReadStream, existsSync, mkdirSync, writeFileSync } from "node:fs";
import http from "node:http";
import { extname, resolve, sep } from "node:path";
import { DatabaseSync } from "node:sqlite";

const root = resolve(import.meta.dirname, "..");
const output = resolve(root, "output/verification/idempotency");
mkdirSync(output, { recursive: true });
const dbPath = resolve(output, `fixture-${Date.now()}.sqlite`);
const db = new DatabaseSync(dbPath);
db.exec(`CREATE TABLE intents (
  id INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT UNIQUE NOT NULL, payload TEXT NOT NULL,
  mode TEXT NOT NULL, deleted INTEGER NOT NULL DEFAULT 0
); CREATE TABLE calls (id INTEGER PRIMARY KEY, proxy_id INTEGER NOT NULL, action TEXT NOT NULL);`);
const records = [];
let next = { mode: "ready", drop: false };
const body = async (request) => {
  const chunks = [];
  for await (const chunk of request) chunks.push(chunk);
  return chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : {};
};
const json = (response, status, data) => {
  response.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" });
  response.end(JSON.stringify(data));
};
function dto(row) {
  const payload = JSON.parse(row.payload);
  const ready = row.mode === "ready";
  return { id: row.id, name: payload.name, kind: "warp", enabled: true,
    status: ready ? "healthy" : "failed", warp_phase: ready ? "ready" : "failed",
    egress_ip: ready ? "203.0.113.42" : "" };
}
function evidence() {
  const rows = db.prepare("SELECT id,key,payload,mode,deleted FROM intents ORDER BY id").all();
  const calls = db.prepare("SELECT proxy_id,action,count(*) AS count FROM calls GROUP BY proxy_id,action").all();
  return { kind: "合成 HTTP/SQLite 后端；register 为合成调用计数，无外网注册", dbPath, records, rows, calls };
}
function persist() { writeFileSync(resolve(output, "synthetic-http-evidence.json"), JSON.stringify(evidence(), null, 2)); }
const server = http.createServer(async (request, response) => {
  try {
    const url = new URL(request.url, "http://127.0.0.1:4173");
    if (url.pathname === "/__fixture/bootstrap") {
      response.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store" });
      response.end("<script>localStorage.setItem('artex_token','synthetic-local-only');document.cookie='artex_token=synthetic-local-only; path=/';location.href='/system/proxies/'</script>");
      return;
    }
    if (url.pathname === "/__fixture/evidence") return json(response, 200, evidence());
    if (url.pathname === "/__fixture/mode" && request.method === "POST") {
      next = await body(request); return json(response, 200, next);
    }
    if (url.pathname === "/__fixture/settle" && request.method === "POST") {
      const input = await body(request);
      db.prepare("UPDATE intents SET mode=?, deleted=? WHERE key=?").run(input.mode, input.mode === "deleted" ? 1 : 0, input.key);
      persist(); return json(response, 200, { ok: true });
    }
    if (url.pathname === "/__fixture/stop" && request.method === "POST") {
      json(response, 200, { stopped: true });
      setTimeout(() => { persist(); server.close(); db.close(); }, 100); return;
    }
    if (url.pathname === "/api/proxies" && request.method === "GET") {
      return json(response, 200, { proxies: db.prepare("SELECT * FROM intents WHERE deleted=0").all().map(dto), global_proxy_id: null });
    }
    if (url.pathname === "/api/proxies" && request.method === "POST") {
      const payload = await body(request);
      const key = request.headers["idempotency-key"];
      const record = { method: "POST", path: url.pathname, key, payload };
      records.push(record);
      const respond = (status, result, drop = false) => {
        Object.assign(record, { status, proxy_id: result.proxy_id ?? result.id, operation_status: result.operation_status, response_dropped: drop });
        persist();
        if (drop) { response.destroy(); return; }
        return json(response, status, result);
      };
      if (!/^[0-9a-f-]{36}$/i.test(key ?? "") || payload.accept_terms !== true) return respond(400, { error: "合成校验拒绝" });
      let row = db.prepare("SELECT * FROM intents WHERE key=?").get(key);
      const canonical = JSON.stringify({ name: payload.name, kind: payload.kind, accept_terms: payload.accept_terms });
      let drop = false;
      if (row && row.payload !== canonical) return respond(409, { error: "同 key 异载荷" });
      if (!row) {
        db.exec("BEGIN IMMEDIATE");
        const inserted = db.prepare("INSERT INTO intents(key,payload,mode) VALUES (?,?,?)").run(key, canonical, next.mode);
        db.prepare("INSERT INTO calls(proxy_id,action) VALUES (?, 'register')").run(inserted.lastInsertRowid);
        db.exec("COMMIT");
        row = db.prepare("SELECT * FROM intents WHERE key=?").get(key);
        drop = next.drop === true;
        next = { mode: "ready", drop: false };
      }
      if (row.deleted) return respond(410, { error: "原条目已删除" });
      const proxy = dto(row);
      if (row.mode === "pending" || row.mode === "pending502") {
        return respond(row.mode === "pending502" ? 502 : 202, { error: "合成处理中", proxy_id: row.id, proxy, operation_status: "pending" }, drop);
      }
      if (row.mode === "failed") return respond(502, { error: "合成注册失败", proxy_id: row.id, proxy, operation_status: "failed" }, drop);
      return respond(201, proxy, drop);
    }
    const action = url.pathname.match(/^\/api\/proxies\/(\d+)\/warp\/(\w+)$/);
    if (action) {
      const row = db.prepare("SELECT * FROM intents WHERE id=?").get(Number(action[1]));
      records.push({ method: request.method, path: url.pathname });
      if (!row || row.mode.startsWith("pending")) { persist(); return json(response, 409, { error: "合成处理中，禁止恢复" }); }
      db.prepare("INSERT INTO calls(proxy_id,action) VALUES (?,?)").run(row.id, action[2]);
      persist(); return json(response, 200, dto(row));
    }
    if (url.pathname === "/api/auth/status") return json(response, 200, { initialized: true, authenticated: true });
    if (url.pathname.startsWith("/api/")) return json(response, 200, {});
    const staticRoot = resolve(root, "out");
    const file = resolve(staticRoot, `.${decodeURIComponent(url.pathname)}`, url.pathname.endsWith("/") ? "index.html" : "");
    if (!file.startsWith(staticRoot + sep) || !existsSync(file)) return json(response, 404, { error: "not found" });
    const types = { ".html": "text/html; charset=utf-8", ".js": "application/javascript", ".css": "text/css", ".woff2": "font/woff2", ".png": "image/png", ".txt": "text/plain" };
    response.writeHead(200, { "Content-Type": types[extname(file)] ?? "application/octet-stream" });
    createReadStream(file).pipe(response);
  } catch (error) { json(response, 500, { error: error.message }); }
});
server.listen(4173, "127.0.0.1", () => console.log("合成 HTTP/SQLite 后端就绪：http://127.0.0.1:4173/__fixture/bootstrap；无真实注册"));
