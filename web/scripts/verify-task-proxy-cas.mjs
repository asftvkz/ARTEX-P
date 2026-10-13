// 合成CAS服务端验证；请求取消/冲突/网络未知均不自动重写版本或重复采样inherit。
async (page) => {
  const base = "http://127.0.0.1:4173";
  const check = (condition, message) => { if (!condition) throw new Error(message); };
  const control = async (data) => page.request.post(`${base}/__fixture/control`, { data });
  const evidence = async () => (await page.request.get(`${base}/__fixture/evidence`)).json();
  const puts = async () => (await evidence()).records.filter((r) => r.method === "PUT");
  const trigger = page.getByRole("button", { name: "切换任务代理", exact: true });
  const mode = page.getByLabel("连接方式", { exact: true });
  const save = page.getByRole("button", { name: "保存代理选择", exact: true });
  const reread = page.getByRole("button", { name: "回读任务核对", exact: true });
  const confirm = page.getByRole("button", { name: "确认以当前任务状态重新选择", exact: true });
  await control({ behavior: {}, task: { status: "paused", paused: true, in_flight: 0, queued: false, proxy_revision: 20, proxy_mode: "inherit", proxy_id: 1, proxy: { id: 1, name: "原快照", kind: "http" } }, globalID: 2 });
  await page.reload();
  await trigger.click();
  await mode.selectOption("inherit");
  await control({ task: { proxy_revision: 21, proxy_mode: "direct", proxy_id: null, proxy: null } });
  const start = (await puts()).length;
  await save.click();
  await page.getByText("任务代理已被其他操作更新：", { exact: false }).waitFor();
  check((await puts()).at(-1).body.expected_revision === 20, "后台更新使草稿revision被静默改写");
  await reread.click();
  await confirm.waitFor();
  check(await save.isDisabled(), "CAS核对后自动允许重放旧意图");
  check(await mode.inputValue() === "inherit", "CAS冲突丢失原草稿");
  check((await puts()).length === start + 1, "CAS回读后重复PUT");
  await page.screenshot({ path: "output/verification/task-proxy-switch/cas-conflict-confirmation.png", fullPage: true });
  await confirm.click();
  check((await puts()).length === start + 1, "确认新意图按钮不应自行保存");
  await save.click();
  await page.getByRole("dialog", { name: "切换任务代理", exact: true }).waitFor({ state: "hidden" });
  check((await puts()).at(-1).body.expected_revision === 21, "明确新意图未使用最新版本");
  check((await evidence()).task.proxy_revision === 22, "成功未推进版本");
  // 首PUT实际完成但页面收不到响应，global随后改变；只GET核对不能再次采样global。
  await trigger.click();
  await mode.selectOption("inherit");
  let dropped = false;
  await page.route("**/api/tasks/12/proxy", async (route) => {
    if (!dropped) { dropped = true; await route.fetch(); await route.abort("failed"); }
    else await route.continue();
  });
  await save.click();
  await reread.waitFor();
  await page.unroute("**/api/tasks/12/proxy");
  const afterUnknown = (await puts()).length;
  await control({ globalID: 4 });
  await reread.click();
  await confirm.waitFor();
  check((await puts()).length === afterUnknown, "未知结果GET后自动PUT");
  check((await evidence()).task.proxy_id === 2, "旧inherit意图重新采样了全局");
  check(await save.isDisabled(), "未知结果核对后应明确确认新意图");
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await trigger.click();
  await confirm.waitFor();
  check(await save.isDisabled(), "关闭重开绕过新意图确认");
  await page.screenshot({ path: "output/verification/task-proxy-switch/cas-unknown-no-resample.png", fullPage: true });
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await control({ task: { proxy_revision: null } });
  await page.reload();
  await trigger.click();
  await page.getByText("任务代理版本缺失或无效", { exact: false }).waitFor();
  check(await save.isDisabled(), "缺revision错误当作0");
  await page.screenshot({ path: "output/verification/task-proxy-switch/cas-missing-blocked.png", fullPage: true });
  await page.getByRole("button", { name: "取消", exact: true }).click();
  return { synthetic: true, passed: true, conflict_sent_revision: 20, explicit_new_revision: 21, unknown_no_reput: true, inherit_snapshot_after_global_changed: 2, invalid_revision_no_put: true };
}
