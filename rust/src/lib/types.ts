/**
 * 与 Rust 侧一一对应的类型。
 *
 * 形状由 `crates/apw-core/tests/wire_format.rs` 钉住 —— 那些用例断言的就是这里
 * 写的结构。谁改了 Rust 的 serde 标注，那边会先红，而不是等界面上莫名少了一列
 * 才发现。
 *
 * 最关键的是 `Availability`：它在 Rust 里是「未知必然携带原因」的枚举，序列化
 * 后扁平成两层判别字段。配合下面的穷尽性检查，Rust 那边新增一个状态、这边漏了
 * 处理就直接编译不过 —— 这条不变量因此从 Rust 一路延伸到了界面上。
 */

export type UnknownReason =
  | { reason: "not_yet_checked" }
  | { reason: "blocked"; detail: string }
  | { reason: "rate_limited" }
  | { reason: "schema_drift"; field: string; raw: string }
  | { reason: "apple_error"; message: string }
  | { reason: "transport"; detail: string };

export type Availability =
  | { kind: "in_stock" }
  | { kind: "out_of_stock" }
  | ({ kind: "unknown" } & UnknownReason);

export interface Target {
  locale: string;
  storeNumber: string;
  storeTitle: string;
  partNumber: string;
  productName: string;
}

export interface TargetState {
  target: Target;
  availability: Availability;
  lastCheckedMs: number | null;
  consecutiveFailures: number;
}

export interface Region {
  title: string;
  locale: string;
}

export type WatcherEvent =
  | { type: "stateChanged"; state: TargetState }
  | { type: "inStock"; state: TargetState }
  | { type: "cycleComplete"; healthy: boolean; snapshot: TargetState[] }
  | { type: "trouble"; reason: string }
  | { type: "runStateChanged"; running: boolean };

/** 走到这里说明有分支没处理。放在 `default` 里，漏掉的分支会变成编译错误。 */
export function assertNever(x: never): never {
  throw new Error(`未处理的分支：${JSON.stringify(x)}`);
}

/** 界面上用于区分的四种展示状态。 */
export type StatusTone = "inStock" | "outOfStock" | "unknown" | "pending";

/**
 * 把状态翻译成展示用的信息。
 *
 * 「未知（出故障）」和「待查询」必须分开：前者意味着这一行的数据不可信，
 * 要用醒目的方式呈现；后者只是还没轮到，弱化即可。上游把这两种和「无货」
 * 全糊成一种显示，用户无从分辨程序是在正常工作还是已经废了。
 */
export function describeAvailability(a: Availability): {
  label: string;
  tone: StatusTone;
  detail: string | null;
} {
  switch (a.kind) {
    case "in_stock":
      return { label: "有货", tone: "inStock", detail: null };
    case "out_of_stock":
      return { label: "无货", tone: "outOfStock", detail: null };
    case "unknown":
      return describeUnknown(a);
    default:
      return assertNever(a);
  }
}

function describeUnknown(a: { kind: "unknown" } & UnknownReason): {
  label: string;
  tone: StatusTone;
  detail: string;
} {
  switch (a.reason) {
    case "not_yet_checked":
      return { label: "待查询", tone: "pending", detail: "尚未轮到这一项" };
    case "blocked":
      return {
        label: "未知",
        tone: "unknown",
        detail: `请求被 Apple 拦截：${a.detail}`,
      };
    case "rate_limited":
      return {
        label: "未知",
        tone: "unknown",
        detail: "请求过于频繁被限流，正在退避",
      };
    case "schema_drift":
      return {
        label: "未知",
        tone: "unknown",
        detail: `接口返回结构与预期不符：${a.field} = ${a.raw}`,
      };
    case "apple_error":
      return { label: "未知", tone: "unknown", detail: `Apple 返回错误：${a.message}` };
    case "transport":
      return { label: "未知", tone: "unknown", detail: `网络请求失败：${a.detail}` };
    default:
      return assertNever(a);
  }
}

/** 这一行的数据是否已经不可信。 */
export function isUntrusted(a: Availability): boolean {
  return a.kind === "unknown" && a.reason !== "not_yet_checked";
}

export function formatTime(ms: number | null): string {
  if (ms === null) return "—";
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}
