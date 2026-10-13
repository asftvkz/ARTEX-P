// 合成 HTTP 延迟旧 GET，验证任务轮询不能覆盖保存后新 GET 的权威结果。
async (page) => {
  const base = "http://127.0.0.1:4173";
  await page.request.post(`${base}/__fixture/control`, { data: {
    behavior: {}, task: { status: "paused", paused: true, in_flight: 0, queued: false, proxy_revision: 30, proxy_mode: "fixed", proxy_id: 1, proxy: { id: 1, name: "原快照", kind: "http" } },
  } });
  await page.reload();
  await page.getByRole("button", { name: "切换任务代理", exact: true }).waitFor();
  let held = false;
  let released = false;
  let release;
  let acknowledge;
  const gate = new Promise((resolve) => { release = resolve; });
  const captured = new Promise((resolve) => { acknowledge = resolve; });
  await page.route("**/api/tasks/12", async (route) => {
    if (!held && route.request().method() === "GET") {
      held = true;
      const old = await route.fetch();
      acknowledge();
      await gate;
      await route.fulfill({ response: old });
      released = true;
    } else await route.continue();
  });
  // 等待最多一个5秒轮询周期；没有发额外任务读取请求。
  await captured;
  try {
    await page.getByRole("button", { name: "切换任务代理", exact: true }).click();
    await page.getByLabel("连接方式", { exact: true }).selectOption("direct");
    await page.getByRole("button", { name: "保存代理选择", exact: true }).click();
    await page.getByRole("dialog", { name: "切换任务代理", exact: true }).waitFor({ state: "hidden", timeout: 10000 });
    const badge = page.getByRole("button", { name: "切换任务代理", exact: true });
    if (!(await badge.innerText()).includes("直连")) throw new Error("新GET未更新徽标");
    release();
    // 必须让旧网络响应实际到达页面，再观察 React 提交后的文字。
    while (!released) await new Promise((resolve) => setTimeout(resolve, 20));
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    if (!(await badge.innerText()).includes("直连")) throw new Error("迟到旧GET覆盖了新代理选择");
    await page.screenshot({ path: "output/verification/task-proxy-switch/stale-poll-blocked.png", fullPage: true });
    return { synthetic: true, passed: true, old_get_delivered_after_save: true, final_badge: await badge.innerText() };
  } finally {
    release();
    await page.unroute("**/api/tasks/12");
  }
}
