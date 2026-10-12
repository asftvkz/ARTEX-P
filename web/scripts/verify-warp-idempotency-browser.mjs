// 由 playwright-cli run-code --filename 执行；先启动合成后端，单次验收上限 60 秒。
async (page) => {
  const check = (condition, message) => { if (!condition) throw new Error(message); };
  const base = "http://127.0.0.1:4173";
  const evidence = async () => (await page.request.get(`${base}/__fixture/evidence`)).json();
  const mode = async (value) => page.request.post(`${base}/__fixture/mode`, { data: value });
  await page.setViewportSize({ width: 1400, height: 1050 });
  await page.getByRole("combobox", { name: "类型", exact: true }).selectOption("warp");
  await page.getByRole("textbox", { name: "名称", exact: true }).fill("合成 socket-destroy 一次点击");
  await mode({ mode: "ready", drop: true });
  // 只观测 fetch 调用与发送前存储，仍调用原生 fetch，不 mock/拦截网络。
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
  await page.getByRole("button", { name: "已完成添加", exact: true }).waitFor({ timeout: 10000 });
  const first = await evidence();
  const row = first.rows.find((row) => JSON.parse(row.payload).name === "合成 socket-destroy 一次点击");
  const requests = first.records.filter((r) => r.key === row.key);
  const fetches = await page.evaluate(() => window.__warpFetches);
  check(fetches.length === 1, "一次 UI 点击应只调用一次原生 fetch");
  check(requests.length >= 2, "未复现底层重发，不能宣称 socket-destroy 双 POST 验收通过");
  check(first.rows.filter((r) => r.key === row.key).length === 1, "同 key 必须只有一行");
  check(first.calls.find((r) => r.proxy_id === row.id && r.action === "register")?.count === 1, "合成 register 必须一次");
  check(requests.every((r) => r.key === fetches[0].key && JSON.stringify(r.payload) === JSON.stringify(fetches[0].payload)), "首发与重发 key/payload 不一致");
  check(fetches[0].stored.key === row.key && fetches[0].stored.accept_terms === true, "发送前未持久接受条款与 key");
  check(await page.getByRole("button", { name: "已完成添加", exact: true }).isDisabled(), "完成态不能继续添加");
  await page.screenshot({ path: "output/verification/idempotency/socket-destroy-completed.png", fullPage: true });
  const before = first.records.length;
  await page.reload();
  await page.getByRole("button", { name: "使用原请求重试（同意条款）", exact: true }).waitFor();
  check((await evidence()).records.length === before, "刷新不能偷偷发送请求或接受条款");
  await page.getByRole("button", { name: "使用原请求重试（同意条款）", exact: true }).click();
  await page.getByRole("button", { name: "已完成添加", exact: true }).waitFor();
  check((await evidence()).rows.length === first.rows.length, "刷新重试不应建新行");
  await page.getByRole("button", { name: "新增另一个", exact: true }).click();
  check(await page.getByRole("textbox", { name: "名称", exact: true }).inputValue() === "", "显式新建应清空名称");
  await page.getByRole("textbox", { name: "名称", exact: true }).fill("合成同步双击第二个");
  // 两次 click 在同一个事件循环触发，覆盖 React 更新尚未完成的窗口。
  await page.getByRole("button", { name: "确认添加（同意条款）", exact: true }).evaluate((button) => { button.click(); button.click(); });
  await page.getByRole("button", { name: "已完成添加", exact: true }).waitFor();
  const final = await evidence();
  const second = final.rows.find((r) => JSON.parse(r.payload).name === "合成同步双击第二个");
  check(second && second.key !== row.key, "明确新增另一个必须使用新 key");
  check(final.records.filter((r) => r.key === second.key).length === 1, "同事件循环双击发送了多个请求");
  check(final.rows.length === first.rows.length + 1, "不同名称新意图应恰好新增一行");
  await page.screenshot({ path: "output/verification/idempotency/explicit-new-intent.png", fullPage: true });
  return { passed: true, synthetic: true, single_click_fetches: fetches.length, socket_destroy_posts: requests.length, first_id: row.id, first_key: row.key, first_rows: 1, first_register_calls: 1, refreshed_same_key: true, explicit_new_id: second.id, explicit_new_key: second.key, same_loop_double_click_posts: 1 };
}
