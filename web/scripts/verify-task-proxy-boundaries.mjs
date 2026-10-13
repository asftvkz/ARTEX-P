// 任务生命周期与不确定写入的合成界面证据；不代表服务端跨 Manager 屏障验证。
async (page) => {
  const base = "http://127.0.0.1:4173";
  const check = (condition, message) => { if (!condition) throw new Error(message); };
  const control = async (data) => page.request.post(`${base}/__fixture/control`, { data });
  const evidence = async () => (await page.request.get(`${base}/__fixture/evidence`)).json();
  const count = async () => (await evidence()).records.filter((r) => r.method === "PUT").length;
  const trigger = page.getByRole("button", { name: "切换任务代理", exact: true });
  const mode = page.getByLabel("连接方式", { exact: true });
  const save = page.getByRole("button", { name: "保存代理选择", exact: true });
  const results = [];
  for (const patch of [{ status: "running", paused: false, in_flight: 0 }, { status: "queued", queued: true }, { status: "paused", paused: true, queued: false, in_flight: 2 }]) {
    await control({ behavior: {}, task: patch });
    await page.reload();
    await trigger.click();
    await mode.selectOption("direct");
    check(await save.isDisabled(), `${patch.status}不应允许保存`);
    results.push({ task: patch, save_disabled: true });
    await page.getByRole("button", { name: "取消", exact: true }).click();
  }
  await control({ behavior: { saveFailure: 409 }, task: { status: "done", paused: false, queued: false, in_flight: 0 } });
  await page.reload();
  await trigger.click();
  await mode.selectOption("direct");
  await save.click();
  await page.getByText("暂不能切换：", { exact: false }).waitFor();
  check(await mode.inputValue() === "direct", "终态409丢失草稿");
  results.push({ terminal_409_authoritative: true });
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await control({ behavior: {}, task: { status: "paused", paused: true, proxy_mode: "fixed", proxy_id: 99, proxy: { id: 99, kind: "http", name: "原失效快照" } } });
  await page.reload();
  await trigger.click();
  await page.getByText("原选择 #99 已失效", { exact: false }).waitFor();
  check(await save.isDisabled(), "原缺失选择不能回落直连");
  check(await mode.inputValue() === "fixed", "原缺失选择模式被更改");
  await page.screenshot({ path: "output/verification/task-proxy-switch/missing-original.png", fullPage: true });
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await control({ behavior: { saveFailure: 500 }, task: { proxy_mode: "inherit", proxy_id: 1, proxy: { id: 1, kind: "http", name: "原全局快照" } } });
  await page.reload();
  await trigger.click();
  await mode.selectOption("direct");
  await save.click();
  const reread = page.getByRole("button", { name: "回读任务核对", exact: true });
  await reread.waitFor();
  const before = await count();
  check(await mode.inputValue() === "direct", "500丢失草稿");
  await reread.click();
  await page.getByText("已核对服务端当前选择", { exact: false }).waitFor();
  check(await mode.inputValue() === "direct", "核对GET丢失用户草稿");
  check(await count() === before, "不确定状态回读重复PUT");
  await page.screenshot({ path: "output/verification/task-proxy-switch/unknown-save-readback.png", fullPage: true });
  await page.getByRole("button", { name: "取消", exact: true }).click();
  return { synthetic: true, passed: true, results, invalid_original_preserved: true, unknown_save_only_get: true };
}
