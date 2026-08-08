/**
 * 界面状态的唯一入口。
 *
 * 有一条铁律：**前端不持有真源**。这里存的每一样东西要么是 Rust 推过来的原样
 * 副本，要么是纯粹的展示状态（比如日志文本）。绝不在前端自己判断「这个型号
 * 到底有没有货」—— 一旦前端有了自己的一份判断，那条花大力气在 Rust 里守住的
 * 不变量就会从前门溜回来。
 *
 * 用 `useSyncExternalStore` 而不是 Context 或状态库：它就是为「订阅外部数据源」
 * 设计的，而我们的数据源正是 Rust。再套一层状态库只会制造「再存一份」的诱惑。
 */

import { invoke } from "@tauri-apps/api/core";
import { listen, type UnlistenFn } from "@tauri-apps/api/event";

import type {
  Product,
  Region,
  Settings,
  Store,
  Target,
  TargetState,
  WatcherEvent,
} from "./types";
import { assertNever, describeAvailability, isUntrusted } from "./types";

const EVENT_CHANNEL = "watcher://event";
const NOTICE_CHANNEL = "watcher://notice";
const MAX_LOG_LINES = 300;

export interface UiState {
  rows: TargetState[];
  running: boolean;
  /** 非 null 表示「当前的状态不可信」，界面要挂一条持续可见的告警。 */
  trouble: string | null;
  logs: string[];
  regions: Region[];
  stores: Store[];
  products: Product[];
  settings: Settings;
  /** 正在从 Apple 官网刷新型号列表。 */
  refreshing: boolean;
  ready: boolean;
}

const DEFAULT_SETTINGS: Settings = {
  locale: "zh_CN",
  targets: [],
  intervalSeconds: 30,
  barkUrl: "",
  soundEnabled: true,
  openBagOnHit: true,
};

let state: UiState = {
  rows: [],
  running: false,
  trouble: null,
  logs: [],
  regions: [],
  stores: [],
  products: [],
  settings: DEFAULT_SETTINGS,
  refreshing: false,
  ready: false,
};

const listeners = new Set<() => void>();

/**
 * 返回缓存的引用。
 *
 * `useSyncExternalStore` 用 `Object.is` 比较，每次返回新对象会导致无限重渲染，
 * 所以只在真正变更时才替换 `state`。
 */
function getSnapshot(): UiState {
  return state;
}

function subscribe(cb: () => void): () => void {
  listeners.add(cb);
  return () => {
    listeners.delete(cb);
  };
}

function update(patch: Partial<UiState>): void {
  state = { ...state, ...patch };
  for (const cb of listeners) cb();
}

function pushLog(line: string): void {
  const stamp = new Date().toLocaleTimeString("zh-CN", { hour12: false });
  const next = [...state.logs, `[${stamp}] ${line}`];
  // 定长保留。上游把日志无限拼进一个字符串，跑一整天能有几 MB，
  // 每次刷新都要重新排版，界面越用越卡。
  update({ logs: next.length > MAX_LOG_LINES ? next.slice(-MAX_LOG_LINES) : next });
}

function applyEvent(event: WatcherEvent): void {
  switch (event.type) {
    case "stateChanged": {
      // 列表本身以 cycleComplete 带来的快照为准，不拿这条事件去增量改 ——
      // 它是可丢弃的，用它做增量会让界面和引擎慢慢对不上。
      // 这里只做一件事：把「某一行开始不可信」记进日志，附上具体原因，
      // 否则用户只能看到一个「未知」，不知道到底出了什么事。
      const { availability, target } = event.state;
      if (isUntrusted(availability)) {
        const { detail } = describeAvailability(availability);
        pushLog(`${target.storeTitle} ${target.productName}：${detail ?? "查询失败"}`);
      }
      break;
    }

    case "inStock":
      pushLog(`有货！${event.state.target.storeTitle} ${event.state.target.productName}`);
      break;

    case "cycleComplete": {
      const recovered = event.healthy && state.trouble !== null;
      update({
        rows: event.snapshot,
        // 只有引擎明说本轮健康，才收起告警。用「所有行都没错误」去反推是
        // 不可靠的：某些故障路径下状态压根没被更新。
        trouble: event.healthy ? null : state.trouble,
      });
      if (recovered) pushLog("查询已恢复正常。");
      break;
    }

    case "trouble":
      update({ trouble: event.reason });
      pushLog(`告警：${event.reason}`);
      break;

    case "runStateChanged":
      update({ running: event.running });
      pushLog(event.running ? "已开始监控。" : "已暂停监控。");
      break;

    default:
      assertNever(event);
  }
}

let unlisteners: UnlistenFn[] = [];
let starting: Promise<void> | null = null;

/**
 * 连接后端。重复调用是安全的。
 *
 * React 开发模式下 StrictMode 会把 effect 执行两次，如果每次都注册监听器，
 * 会得到两份 —— 日志每行打两遍、提醒重复触发。这个问题只在 dev 复现、生产不会，
 * 最容易漏到很后面才发现，所以这里用一个进行中的 Promise 做去重。
 */
export function connect(): Promise<void> {
  if (starting) return starting;
  starting = (async () => {
    unlisteners = await Promise.all([
      listen<WatcherEvent>(EVENT_CHANNEL, (e) => applyEvent(e.payload)),
      listen<string>(NOTICE_CHANNEL, (e) => pushLog(`启动提示：${e.payload}`)),
    ]);

    const [regions, settings, rows, running] = await Promise.all([
      invoke<Region[]>("list_regions"),
      invoke<Settings>("get_settings"),
      invoke<TargetState[]>("get_snapshot"),
      invoke<boolean>("is_running"),
    ]);
    update({ regions, settings, rows, running, ready: true });
    await loadCatalog(settings.locale);
  })();
  return starting;
}

export function disconnect(): void {
  for (const un of unlisteners) un();
  unlisteners = [];
  starting = null;
}

export const watcherStore = { subscribe, getSnapshot };

// ---- 命令。全部只是转发，不含任何业务判断。

/** 载入某地区的门店与型号目录。 */
export async function loadCatalog(locale: string): Promise<void> {
  try {
    const [stores, products] = await Promise.all([
      invoke<Store[]>("list_stores", { locale }),
      invoke<Product[]>("list_products", { locale }),
    ]);
    update({ stores, products });
  } catch (err) {
    // 目录读不出来不该让整个界面挂掉，但必须让用户知道下拉为什么是空的。
    update({ stores: [], products: [] });
    pushLog(`载入 ${locale} 的门店与型号失败：${String(err)}`);
  }
}

export async function saveSettings(next: Settings): Promise<void> {
  try {
    const saved = await invoke<Settings>("save_settings", { settings: next });
    update({ settings: saved });
  } catch (err) {
    pushLog(`保存设置失败：${String(err)}`);
  }
}

export async function changeLocale(locale: string): Promise<void> {
  await saveSettings({ ...state.settings, locale });
  await loadCatalog(locale);
}

export async function setTargets(targets: Target[]): Promise<void> {
  try {
    const rows = await invoke<TargetState[]>("set_targets", { targets });
    update({ rows, settings: { ...state.settings, targets } });
  } catch (err) {
    pushLog(`更新监控列表失败：${String(err)}`);
  }
}

export async function startWatching(): Promise<void> {
  await invoke("start_watching");
}

export async function stopWatching(): Promise<void> {
  await invoke("stop_watching");
}

export async function setIntervalSeconds(seconds: number): Promise<void> {
  try {
    const applied = await invoke<number>("set_interval", { seconds });
    update({ settings: { ...state.settings, intervalSeconds: applied } });
  } catch (err) {
    pushLog(`设置查询间隔失败：${String(err)}`);
  }
}

/** 从 Apple 官网抓最新型号。 */
export async function refreshProducts(): Promise<void> {
  if (state.refreshing) return;
  update({ refreshing: true });
  const locale = state.settings.locale;
  try {
    const count = await invoke<number>("refresh_products", { locale });
    pushLog(`已从 Apple 官网更新 ${count} 个型号。`);
    await loadCatalog(locale);
  } catch (err) {
    // 抓取失败仍可继续用内嵌的离线目录，只是可能缺最新机型。
    pushLog(`更新型号列表失败（仍可使用内置目录）：${String(err)}`);
  } finally {
    update({ refreshing: false });
  }
}

export async function testNotify(): Promise<void> {
  try {
    await invoke("test_notify");
    pushLog("已发出测试提醒。");
  } catch (err) {
    pushLog(`测试提醒失败：${String(err)}`);
  }
}
