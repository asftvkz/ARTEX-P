// 仅一个真实 Go/PG 断响应场景；外部 WARP 工具已在专用后端进程中禁用。
async (page) => {
  const check = (condition, message) => { if (!condition) throw new Error(message); };
  await page.setViewportSize({ width: 1400, height: 1000 });
  await page.getByRole("combobox", { name: "类型", exact: true }).selectOption("warp");
  await page.getByRole("textbox", { name: "名称", exact: true }).fill("真实GoPG断响应幂等验收");
  await page.evaluate(() => {
    const original = window.fetch;
    window.__warpFetches = [];
    window.fetch = function(url, init) {
      if (url === "/api/proxies" && init?.method === "POST") {
        window.__warpFetches.push({ key: init.headers["Idempotency-Key"], payload: JSON.parse(init.body), stored: JSON.parse(sessionStorage.getItem("artex.warp-create-intent.v1")) });
      }
      return original.call(this, url, init);
    };
  });
  await page.getByRole("button", { name: "确认添加（同意条款）", exact: true }).click();
  await page.getByRole("button", { name: /恢复此出口/ }).waitFor({ timeout: 15000 });
  const result = await (await page.request.get("http://127.0.0.1:4173/__real/evidence")).json();
  const fetches = await page.evaluate(() => window.__warpFetches);
  check(fetches.length === 1, "一次 UI 点击调用了不止一次 fetch");
  check(result.records.length >= 2, "未复现真实 socket destroy 底层双 POST");
  const first = result.records[0];
  check(first.response_dropped_after_backend_completed === true, "首响应未在真实后端完整返回后销毁");
  check(result.records.every((r) => r.key === first.key && r.forwarded_key === first.key && r.proxy_id === first.proxy_id && r.status === 502 && r.operation_status === "failed"), "重放 key/ID/502 failed 不一致");
  check(fetches[0].stored.key === first.key && fetches[0].stored.accept_terms === true, "首发前未保存意图和条款接受");
  check(await page.getByRole("button", { name: `恢复此出口 #${first.proxy_id}` }).isEnabled(), "未保留失败原 ID");
  await page.screenshot({ path: "output/verification/idempotency/real-go-pg-same-id.png", fullPage: true });
  return { passed: true, backend: "真实生产 Go 二进制与隔离 PG", database: result.database, ui_fetch_calls: fetches.length, http_posts: result.records.length, key: first.key, proxy_id: first.proxy_id, statuses: result.records.map((r) => r.status), operation_status: "failed", pg_count: "随后使用 psql 独立查询", external_registration: "禁用 sh/wgcf/wireproxy，未执行真实外网注册" };
}
