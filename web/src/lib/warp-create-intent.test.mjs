import { WARP_INTENT_STORAGE_KEY, WarpCreateIntent } from "./warp-create-intent.ts";
import assert from "node:assert/strict";
import test from "node:test";

const key1 = "11000000-0000-4000-8000-000000000001";
const key2 = "11000000-0000-4000-8000-000000000002";
function fixture() {
  const values = new Map();
  const storage = {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  };
  let generated = 0;
  const create = () =>
    new WarpCreateIntent(
      () => storage,
      () => [key1, key2][generated++],
    );
  return { values, storage, create, generated: () => generated };
}

test("同一事件循环双提交、失败重试、重复初始化均沿用一个 key", () => {
  const f = fixture();
  const flow = f.create();
  flow.restore();
  assert.deepEqual(flow.begin(" 出口一 ", true), { name: "出口一", key: key1, kind: "warp", accept_terms: true });
  assert.equal(flow.begin("出口一", true), null);
  flow.finish("uncertain");
  flow.restore();
  assert.equal(flow.begin("出口一", true).key, key1);
  assert.equal(f.generated(), 1);
  assert.deepEqual(JSON.parse(f.storage.getItem(WARP_INTENT_STORAGE_KEY)), {
    name: "出口一",
    key: key1,
    kind: "warp",
    accept_terms: true,
  });
});

test("刷新只恢复名称/key，已提交意图不能变更 payload，未知结果可以原 key 重试", () => {
  const f = fixture();
  const first = f.create();
  first.restore();
  first.begin("出口一", true);
  const refreshed = f.create();
  assert.deepEqual(refreshed.restore(), { name: "出口一", key: key1, kind: "warp", accept_terms: true });
  assert.equal(refreshed.stage, "uncertain");
  assert.throws(() => refreshed.begin("出口二", true), /新增另一个/);
  assert.equal(refreshed.begin("出口一", true).key, key1);
  assert.equal(f.generated(), 1);
});

for (const stage of ["complete", "retained", "pending", "conflict", "deleted"]) {
  test(`${stage} 不允许重复创建；显式新增另一个才使用新 key`, () => {
    const f = fixture();
    const flow = f.create();
    flow.restore();
    flow.begin("出口一", true);
    flow.finish(stage);
    assert.equal(flow.begin("出口一", true), null);
    assert.equal(f.generated(), 1);
    flow.newIntent();
    assert.equal(flow.begin("出口二", true).key, key2);
    assert.equal(f.generated(), 2);
  });
}

test("成功刷新也保留原 key；不会把旧名称自动变为新的创建请求", () => {
  const f = fixture();
  const flow = f.create();
  flow.restore();
  flow.begin("出口一", true);
  flow.finish("complete");
  const refreshed = f.create();
  refreshed.restore();
  assert.equal(refreshed.begin("出口一", true).key, key1);
  assert.equal(f.generated(), 1);
});

test("sessionStorage getter/getItem 抛异常时不能生成 key 或开始请求", () => {
  for (const storage of [
    () => {
      throw new Error("拒绝访问");
    },
    () => ({
      getItem() {
        throw new Error("读取失败");
      },
    }),
  ]) {
    let generated = 0;
    const flow = new WarpCreateIntent(storage, () => {
      generated++;
      return key1;
    });
    assert.throws(() => flow.restore());
    assert.throws(() => flow.begin("出口一", true), /存储/);
    assert.equal(generated, 0);
  }
});

test("持久化失败或损坏意图均失败闭锁；显式新建清理失败不能丢掉原 key", () => {
  const f = fixture();
  const flow = f.create();
  flow.restore();
  f.storage.setItem = () => {
    throw new Error("配额不足");
  };
  assert.throws(() => flow.begin("出口一", true));
  assert.equal(flow.inFlight, false);
  assert.throws(() => flow.begin("出口一", true), /存储/);
  f.values.set(
    WARP_INTENT_STORAGE_KEY,
    JSON.stringify({ name: "出口一", key: key1, kind: "warp", accept_terms: true }),
  );
  flow.restore();
  assert.equal(flow.begin("出口一", true).key, key1);
  flow.finish("complete");
  f.storage.removeItem = () => {
    throw new Error("拒绝删除");
  };
  assert.throws(() => flow.newIntent());
  assert.equal(flow.intent.key, key1);
  assert.equal(flow.begin("出口一", true), null);
  const broken = f.create();
  f.values.set(WARP_INTENT_STORAGE_KEY, "{invalid");
  assert.throws(() => broken.restore());
  assert.throws(() => broken.begin("出口二", true), /存储/);
});

test("在途点击新增另一个不替换意图", () => {
  const f = fixture();
  const flow = f.create();
  flow.restore();
  flow.begin("出口一", true);
  assert.equal(flow.newIntent(), false);
  assert.equal(flow.intent.key, key1);
});

test("未明确接受条款不能写入意图或发送；恢复不得补造条款接受", () => {
  const f = fixture();
  const flow = f.create();
  flow.restore();
  assert.throws(() => flow.begin("出口一", false), /条款/);
  assert.equal(f.values.size, 0);
  for (const accepted of [false, undefined]) {
    f.values.set(
      WARP_INTENT_STORAGE_KEY,
      JSON.stringify({ name: "出口一", key: key1, kind: "warp", accept_terms: accepted }),
    );
    assert.throws(() => f.create().restore());
  }
});

test("只有显式新建换 key，旧请求迟到不可应用到新意图", () => {
  const f = fixture();
  const flow = f.create();
  flow.restore();
  const original = flow.begin("出口一", true);
  flow.finish("uncertain");
  flow.newIntent();
  const next = flow.begin("出口二", true);
  assert.equal(flow.isCurrent(original), false);
  assert.equal(flow.isCurrent(next), true);
});
