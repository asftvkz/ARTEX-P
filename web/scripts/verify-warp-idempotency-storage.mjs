// 浏览器网络未知与存储异常验收，合成后端不发起外网请求。
async (page) => {
  const check = (condition, message) => { if (!condition) throw new Error(message); };
  const base = "http://127.0.0.1:4173";
  const evidence = async () => (await page.request.get(`${base}/__fixture/evidence`)).json();
  await page.getByRole("button", { name: "新增另一个", exact: true }).click();
  await page.getByRole("textbox", { name: "名称", exact: true }).fill("合成网络未知刷新恢复");
  let intercepted = false;
  await page.route("**/api/proxies", async (route) => {
    if (route.request().method() === "POST" && !intercepted) {
      intercepted = true;
      await route.fetch();
      await route.abort("failed");
    } else await route.continue();
  });
  await page.getByRole("button", { name: "确认添加（同意条款）", exact: true }).click();
  await page.getByRole("button", { name: "使用原请求重试（同意条款）", exact: true }).waitFor();
  await page.unroute("**/api/proxies");
  const original = await page.evaluate(() => JSON.parse(sessionStorage.getItem("artex.warp-create-intent.v1")));
  const before = await evidence();
  await page.screenshot({ path: "output/verification/idempotency/network-unknown.png", fullPage: true });
  await page.reload();
  await page.getByRole("button", { name: "使用原请求重试（同意条款）", exact: true }).waitFor();
  check((await evidence()).records.length === before.records.length, "刷新自动发送了请求");
  const restored = await page.evaluate(() => JSON.parse(sessionStorage.getItem("artex.warp-create-intent.v1")));
  check(JSON.stringify(original) === JSON.stringify(restored), "刷新改变了 key/payload/条款接受");
  await page.getByRole("button", { name: "使用原请求重试（同意条款）", exact: true }).click();
  await page.getByRole("button", { name: "已完成添加", exact: true }).waitFor();
  const after = await evidence();
  check(after.rows.length === before.rows.length, "未知结果恢复重建了记录");
  check(after.records.filter((r) => r.key === original.key).length === 2, "未知恢复应仅首次与明确重试两次请求");
  const storageResults = [];
  for (const fault of ["getter", "write"]) {
    const isolated = await page.context().newPage();
    await isolated.addInitScript((fault) => {
      if (fault === "getter") Object.defineProperty(window, "sessionStorage", { get() { throw new Error("合成拒绝读取"); } });
      else {
        const originalSet = Storage.prototype.setItem;
        Storage.prototype.setItem = function(key, value) {
          if (key === "artex.warp-create-intent.v1") throw new Error("合成写入失败");
          return originalSet.call(this, key, value);
        };
      }
    }, fault);
    const count = (await evidence()).records.length;
    await isolated.goto(`${base}/system/proxies/`);
    await isolated.getByRole("combobox", { name: "类型", exact: true }).selectOption("warp");
    await isolated.getByRole("textbox", { name: "名称", exact: true }).fill(`合成存储${fault}异常`);
    const submit = isolated.getByRole("button", { name: "确认添加（同意条款）", exact: true });
    if (fault === "write") await submit.click();
    await isolated.getByRole("button", { name: "重试读取本地意图", exact: true }).waitFor();
    check(await submit.isDisabled(), `${fault}异常应阻止发送`);
    check((await evidence()).records.length === count, `${fault}异常发出了创建请求`);
    await isolated.screenshot({ path: `output/verification/idempotency/storage-${fault}-blocked.png`, fullPage: true });
    await isolated.close();
    storageResults.push({ fault, passed: true, post_count: 0 });
  }
  return { passed: true, synthetic: true, network_unknown_same_key: original.key, refresh_no_post: true, storage: storageResults };
}
