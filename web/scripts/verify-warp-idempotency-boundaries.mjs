// 与主浏览器验收使用同一命名会话；每场景仅访问本地合成后端。
async (page) => {
  const check = (condition, message) => { if (!condition) throw new Error(message); };
  const base = "http://127.0.0.1:4173";
  const evidence = async () => (await page.request.get(`${base}/__fixture/evidence`)).json();
  const control = async (path, data) => page.request.post(`${base}/__fixture/${path}`, { data });
  const current = async () => page.evaluate(() => JSON.parse(sessionStorage.getItem("artex.warp-create-intent.v1")));
  const report = [];
  for (const mode of ["pending", "pending502"]) {
    await page.getByRole("button", { name: "新增另一个", exact: true }).click();
    await page.getByRole("textbox", { name: "名称", exact: true }).fill(`合成 ${mode} 禁恢复`);
    await control("mode", { mode, drop: false });
    await page.getByRole("button", { name: "确认添加（同意条款）", exact: true }).click();
    await page.getByRole("button", { name: /核对进度/ }).waitFor();
    const intent = await current();
    const row = (await evidence()).rows.find((r) => r.key === intent.key);
    await page.getByRole("button", { name: "恢复", exact: true }).waitFor();
    check(await page.getByRole("button", { name: "恢复", exact: true }).isDisabled(), `${mode} 错误开放恢复`);
    check(await page.getByRole("button", { name: "新增另一个", exact: true }).isDisabled(), "pending 不能意外更换 key");
    await page.screenshot({ path: `output/verification/idempotency/${mode}-recover-blocked.png`, fullPage: true });
    await control("settle", { key: intent.key, mode: "ready" });
    await page.getByRole("button", { name: /核对进度/ }).click();
    await page.getByRole("button", { name: "已完成添加", exact: true }).waitFor({ timeout: 10000 });
    const after = await evidence();
    check(after.calls.find((c) => c.proxy_id === row.id && c.action === "register")?.count === 1, "pending 重放重复注册");
    check(!after.calls.some((c) => c.proxy_id === row.id && c.action === "recover"), "pending 发出了 recover");
    check((await current()).key === intent.key, "pending 完成后意图变更");
    report.push({ mode, id: row.id, key: intent.key, passed: true, recover_calls: 0, synthetic_register_calls: 1 });
  }
  await page.getByRole("button", { name: "新增另一个", exact: true }).click();
  await page.getByRole("textbox", { name: "名称", exact: true }).fill("合成 failed 同 ID");
  await control("mode", { mode: "failed", drop: false });
  await page.getByRole("button", { name: "确认添加（同意条款）", exact: true }).click();
  await page.getByRole("button", { name: /恢复此出口/ }).waitFor();
  const failed = await current();
  const row = (await evidence()).rows.find((r) => r.key === failed.key);
  check(await page.getByRole("button", { name: `恢复此出口 #${row.id}` }).isEnabled(), "failed 未保留原 ID 恢复");
  await page.screenshot({ path: "output/verification/idempotency/failed-same-id.png", fullPage: true });
  // 成功意图的运行状态后来失效：重放 201 仍不能新建或自动注册。
  await control("settle", { key: failed.key, mode: "nonready" });
  await page.reload();
  await page.getByRole("button", { name: "使用原请求重试（同意条款）", exact: true }).click();
  await page.getByRole("button", { name: `恢复此出口 #${row.id}` }).waitFor();
  check((await evidence()).calls.filter((c) => c.proxy_id === row.id).length === 1, "201非ready重放另发了注册/恢复");
  await control("settle", { key: failed.key, mode: "deleted" });
  await page.reload();
  await page.getByRole("button", { name: "使用原请求重试（同意条款）", exact: true }).click();
  await page.getByRole("button", { name: "原代理已删除", exact: true }).waitFor();
  check(await page.getByRole("button", { name: "原代理已删除", exact: true }).isDisabled(), "410 意外开放再次创建");
  check((await current()).key === failed.key, "tombstone 意外更换 key");
  await page.screenshot({ path: "output/verification/idempotency/tombstone.png", fullPage: true });
  report.push({ mode: "failed/nonready/tombstone", id: row.id, key: failed.key, passed: true });
  // 人为修改存储载荷模拟同 key 异载荷，界面必须保持冲突，不能换 key 重建。
  await page.evaluate(() => {
    const key = "artex.warp-create-intent.v1";
    const intent = JSON.parse(sessionStorage.getItem(key));
    intent.name = "合成冲突名称";
    sessionStorage.setItem(key, JSON.stringify(intent));
  });
  await page.reload();
  await page.getByRole("button", { name: "使用原请求重试（同意条款）", exact: true }).click();
  await page.getByRole("button", { name: "请核对原请求", exact: true }).waitFor();
  check((await current()).key === failed.key, "409 意外更换 key");
  report.push({ mode: "conflict", passed: true });
  return { synthetic: true, passed: true, cases: report };
}
