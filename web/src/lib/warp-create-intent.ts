export const WARP_INTENT_STORAGE_KEY = "artex.warp-create-intent.v1";
export const WARP_IDEMPOTENCY_KEY = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

export type WarpIntent = Readonly<{ name: string; key: string; kind: "warp"; accept_terms: true }>;
export type WarpCreateStage = "draft" | "uncertain" | "pending" | "retained" | "complete" | "conflict" | "deleted";
type IntentStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;

// 这是同步意图锁；不能依赖 React 的异步状态来防止同一事件循环重复提交。
// 存储只保存规范化创建载荷与 UUID，包含用户明确接受条款，不存任何凭据。
// 成功/失败后均保留，明确新增另一个才清除；恢复本身绝不发请求。
export class WarpCreateIntent {
  intent: WarpIntent | null = null;
  stage: WarpCreateStage = "draft";
  inFlight: boolean;
  private initialized: boolean;

  constructor(
    private readonly storage: () => IntentStorage = () => window.sessionStorage,
    private readonly uuid: () => string = () => crypto.randomUUID(),
  ) {
    this.inFlight = false;
    this.initialized = false;
  }

  get storageReady() {
    return this.initialized;
  }

  isCurrent(intent: WarpIntent) {
    return this.intent?.key === intent.key;
  }

  restore(): WarpIntent | null {
    if (this.initialized) return this.intent;
    const raw = this.storage().getItem(WARP_INTENT_STORAGE_KEY);
    if (raw !== null) {
      const value: unknown = JSON.parse(raw);
      if (
        !value ||
        typeof value !== "object" ||
        !("name" in value) ||
        !("key" in value) ||
        typeof value.name !== "string" ||
        !value.name.trim() ||
        value.name !== value.name.trim() ||
        typeof value.key !== "string" ||
        !WARP_IDEMPOTENCY_KEY.test(value.key) ||
        !("kind" in value) ||
        value.kind !== "warp" ||
        !("accept_terms" in value) ||
        value.accept_terms !== true ||
        Object.keys(value).length !== 4
      )
        throw new Error("本地 WARP 意图存储损坏，请先核对代理列表，勿重复新建");
      this.intent = Object.freeze({ name: value.name, key: value.key, kind: "warp", accept_terms: true });
      this.stage = "uncertain";
    }
    this.initialized = true;
    return this.intent;
  }

  begin(name: string, acceptTerms: boolean): WarpIntent | null {
    if (this.inFlight || !["draft", "uncertain"].includes(this.stage)) return null;
    if (!this.initialized) throw new Error("本地意图存储尚未确认，请先重试读取，勿重复新建");
    if (acceptTerms !== true) throw new Error("请明确接受 WARP 服务条款后再提交");
    const normalized = name.trim();
    if (!normalized) throw new Error("请填写代理名称");
    if (this.intent && this.intent.name !== normalized) throw new Error("原意图名称不可变更，请明确选择新增另一个");
    if (!this.intent) {
      const key = this.uuid();
      if (!WARP_IDEMPOTENCY_KEY.test(key)) throw new Error("无法生成安全的 WARP 请求编号");
      const intent = Object.freeze({ name: normalized, key, kind: "warp" as const, accept_terms: true as const });
      const serialized = JSON.stringify(intent);
      try {
        const storage = this.storage();
        storage.setItem(WARP_INTENT_STORAGE_KEY, serialized);
        if (storage.getItem(WARP_INTENT_STORAGE_KEY) !== serialized) throw new Error("意图存储未保存");
      } catch {
        // 写入也可能部分成功；再次读回之前禁止生成另一个 key 或发出请求。
        this.initialized = false;
        throw new Error("无法保存 WARP 意图，尚未发送请求；请重试读取本地存储");
      }
      this.intent = intent;
    }
    this.inFlight = true;
    return this.intent;
  }

  finish(stage: WarpCreateStage) {
    this.stage = stage;
    this.inFlight = false;
  }

  newIntent(): boolean {
    if (this.inFlight) return false;
    // 清理失败保持原意图，不能仅清理内存造成刷新后使用另一个 key。
    const storage = this.storage();
    storage.removeItem(WARP_INTENT_STORAGE_KEY);
    if (storage.getItem(WARP_INTENT_STORAGE_KEY) !== null) throw new Error("原 WARP 意图存储未清除");
    this.intent = null;
    this.stage = "draft";
    this.initialized = true;
    return true;
  }
}
