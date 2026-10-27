import assert from "node:assert/strict";
import test from "node:test";
import { loadSource } from "./load-source.mjs";

function renderApp() {
  const saved = [];
  const addedTargets = [];
  const pushSaves = [];
  const state = {
    settings: { locale: "zh_CN", barkUrl: "https://api.day.app/saved-key", intervalSeconds: 30, targets: [] },
    stores: [], products: [], rows: [], regions: [], categories: [], logs: [],
    category: "iphone", trouble: null, update: null,
  };
  const hooks = [];
  let cursor = 0;
  // 宿主仅替代 React hooks 与外观组件；执行 App 自身的 JSX 和输入事件处理器。
  const App = loadSource("src/App.tsx", (specifier) => {
    if (specifier === "react") return {
      useEffect() {},
      useMemo: (compute) => compute(),
      useSyncExternalStore: (_subscribe, getSnapshot) => getSnapshot(),
      useState(initial) {
        const index = cursor++;
        if (!(index in hooks)) hooks[index] = initial;
        return [hooks[index], (value) => { hooks[index] = value; }];
      },
    };
    if (specifier === "@/lib/store") return {
      watcherStore: { getSnapshot: () => state },
      saveSettings: async (settings) => { saved.push(settings); state.settings = settings; },
      setTargets: async (targets) => { addedTargets.push(targets); },
      setCategory: (category) => { state.category = category; },
      changeLocale: async (locale) => { state.settings = { ...state.settings, locale }; },
      setPushUrls: async (urls) => {
        pushSaves.push(urls);
        // 模拟后端分栏存放。真正的归类规则在 Rust 侧，由 crates/apw-core/tests/config.rs 覆盖；
        // 这里只需要让界面拿到「哪个地址存在哪一栏」。
        const feishu = urls.filter((url) => url.includes("/open-apis/bot/v2/hook/"));
        const bark = urls.filter((url) => !feishu.includes(url));
        state.settings = { ...state.settings, barkUrl: bark.join(";"), feishuWebhook: feishu.join(";") };
      },
    };
    if (specifier.startsWith("@/components/") || specifier === "lucide-react") {
      return new Proxy({}, { get: (_target, name) => String(name) });
    }
  }).default;
  function control(predicate) {
    cursor = 0;
    const find = (element) => {
      if (!element || typeof element !== "object") return;
      if (predicate(element)) return element;
      const children = Array.isArray(element) ? element : element?.props?.children;
      for (const child of Array.isArray(children) ? children : [children]) {
        const result = find(child);
        if (result) return result;
      }
    };
    const element = find(App());
    assert.ok(element, "expected UI control to be rendered");
    return element.props;
  }
  function all(predicate) {
    cursor = 0;
    const found = [];
    const walk = (element) => {
      if (!element || typeof element !== "object") return;
      if (predicate(element)) found.push(element);
      const children = Array.isArray(element) ? element : element?.props?.children;
      for (const child of Array.isArray(children) ? children : [children]) walk(child);
    };
    walk(App());
    return found;
  }
  function last(predicate) {
    const found = all(predicate);
    assert.ok(found.length > 0, "expected UI control to be rendered");
    return found.at(-1).props;
  }
  const isPushInput = (element) => /^push-\d+$/.test(element?.props?.id ?? "");
  const isAddPush = (element) => element.type === "Button" &&
    Array.isArray(element.props.children) && element.props.children.includes(" 添加推送地址");
  return {
    proxyRow: (index) => control((element) => element?.props?.id === `proxy-${index}`),
    proxyValues: () => all((element) => /^proxy-\d+$/.test(element?.props?.id ?? "")).map((element) => element.props.value),
    removeProxy: (index) => control((element) => element?.props?.["aria-label"] === `删除代理地址 ${index + 1}`),
    rejectedProxies: () => all((element) => element.type === "Badge" && element.props.children === "未生效").length,
    addProxy: () => control((element) => element.type === "Button" &&
      Array.isArray(element.props.children) && element.props.children.includes(" 添加代理")),
    pushRow: (index) => control((element) => element?.props?.id === `push-${index}`),
    pushValues: () => all(isPushInput).map((element) => element.props.value),
    // 渠道标签按行的顺序排列；还没保存的行没有标签。
    pushLabels: () => all((element) => element.type === "Badge" && element.props.children !== "未生效")
      .map((element) => element.props.children),
    removePush: (index) => control((element) => element?.props?.["aria-label"] === `删除推送地址 ${index + 1}`),
    addPush: () => control(isAddPush),
    hasAddPush: () => all(isAddPush).length > 0,
    choice: (placeholder) => control((element) => element?.props?.placeholder === placeholder),
    // 顶栏也有一个「添加」，只负责打开面板；要的是面板底部真正加入目标的那个。
    addButton: () => last((element) => element.type === "Button" &&
      Array.isArray(element.props.children) && element.props.children.includes(" 添加")),
    scaleHint: () => control((element) => element.type === "span" &&
      Array.isArray(element.props.children) && element.props.children.includes(" 个型号 × ")),
    // 顶栏那一行状态，取悬停提示里的整句。
    status: () => control((element) => element.type === "p" &&
      typeof element.props.title === "string" && element.props.title.includes("个目标")).title,
    watchList: () => control((element) => element.type === "WatchList"),
    state, saved, addedTargets, pushSaves,
  };
}

const K1 = "https://api.day.app/k1";
const K2 = "https://api.day.app/k2";
const F1 = "https://open.feishu.cn/open-apis/bot/v2/hook/f1";

test("saved push addresses are one list, Bark first then Feishu, labelled by where the backend filed them", () => {
  const app = renderApp();
  assert.deepEqual(app.pushValues(), ["https://api.day.app/saved-key"]);
  // 没动过时跟着后端走。
  app.state.settings = { ...app.state.settings, barkUrl: `${K1};${K2}`, feishuWebhook: F1 };
  assert.deepEqual(app.pushValues(), [K1, K2, F1]);
  assert.deepEqual(app.pushLabels(), ["Bark", "Bark", "飞书"]);
  // 最后一行有内容才给「添加」，界面上最多只有一个空行。
  assert.equal(app.hasAddPush(), true);
});

test("old settings without push addresses show one empty row that saves on blur", () => {
  const app = renderApp();
  app.state.settings = { locale: "zh_CN", barkUrl: "", intervalSeconds: 30, targets: [] };
  assert.deepEqual(app.pushValues(), [""], "老设置没有 feishuWebhook 字段，也应当是一个空行而不是报错");
  assert.equal(app.hasAddPush(), false);
  assert.equal(app.removePush(0).disabled, true, "只剩一个空行时删除没有意义");
  app.pushRow(0).onChange({ target: { value: `  ${F1}  ` } });
  app.pushRow(0).onBlur();
  assert.deepEqual(app.pushSaves.at(-1), [F1]);
  assert.deepEqual(app.pushValues(), [F1]);
  assert.deepEqual(app.pushLabels(), ["飞书"]);
});

test("adding a row keeps the typed order, splits a pasted semicolon list, and skips saves with no change", () => {
  const app = renderApp();
  app.pushRow(0).onBlur();
  assert.equal(app.pushSaves.length, 0, "没改动就不该写盘");
  app.addPush().onClick();
  assert.deepEqual(app.pushValues(), ["https://api.day.app/saved-key", ""]);
  assert.equal(app.hasAddPush(), false);
  // 老版本用分号连着的一整串粘进了一行：拆成几行，各归各的渠道。
  app.pushRow(1).onChange({ target: { value: ` ${F1};${K2} ` } });
  app.pushRow(1).onBlur();
  assert.deepEqual(app.pushSaves.at(-1), ["https://api.day.app/saved-key", F1, K2]);
  assert.deepEqual(app.pushValues(), ["https://api.day.app/saved-key", F1, K2]);
  assert.deepEqual(app.pushLabels(), ["Bark", "飞书", "Bark"]);
  // 新开的空行没填就离开，这一行直接消失，不写盘。
  app.addPush().onClick();
  app.pushRow(3).onBlur();
  assert.deepEqual(app.pushValues(), ["https://api.day.app/saved-key", F1, K2]);
  assert.equal(app.pushSaves.length, 1);
});

test("deleting a row saves the rest at once, and clearing the last one leaves an empty row", () => {
  const app = renderApp();
  app.state.settings = { ...app.state.settings, barkUrl: K1, feishuWebhook: F1 };
  app.removePush(0).onClick();
  assert.deepEqual(app.pushSaves.at(-1), [F1]);
  assert.deepEqual(app.pushValues(), [F1]);
  assert.equal(app.state.settings.barkUrl, "");
  // 清空也是明确的操作：保存空列表，界面不退回旧地址。
  app.pushRow(0).onChange({ target: { value: "" } });
  app.pushRow(0).onBlur();
  assert.deepEqual(app.pushSaves.at(-1), []);
  assert.deepEqual(app.pushValues(), [""]);
  assert.equal(app.state.settings.feishuWebhook, "");
});

function phoneSelectionApp() {
  const app = renderApp();
  app.state.stores = [{ number: "R532", title: "杭州万象城" }];
  app.state.products = [
    { partNumber: "A", category: "iphone", family: "iphone18pro", capacity: "256GB", color: "黑色", title: "iPhone 18 Pro 256GB 黑色" },
    { partNumber: "B", category: "iphone", family: "iphone18pro", capacity: "512GB", color: "银色", title: "iPhone 18 Pro 512GB 银色" },
    { partNumber: "C", category: "iphone", family: "iphone18promax", capacity: "512GB", color: "银色", title: "iPhone 18 Pro Max 512GB 银色" },
    { partNumber: "D", category: "ipad", family: "ipadpro", capacity: "256GB", color: "银色", title: "iPad Pro 256GB 银色" },
  ];
  app.choice("选择自提门店").onChange(["R532"]);
  app.choice("选择机型").onChange(["iphone18pro"]);
  app.choice("选择容量").onChange(["512GB"]);
  app.choice("选择颜色").onChange(["银色"]);
  return app;
}

test("iPhone choices add the exact monitoring target and reset after adding", async () => {
  const app = phoneSelectionApp();
  assert.equal(app.addButton().disabled, false);
  app.addButton().onClick();
  await new Promise(setImmediate);
  assert.deepEqual(app.addedTargets, [[{
    locale: "zh_CN", storeNumber: "R532", storeTitle: "杭州万象城",
    partNumber: "B", productName: "iPhone 18 Pro 512GB 银色",
  }]]);
  assert.deepEqual(app.choice("选择机型").values, []);
  assert.equal(app.choice("选择容量").disabled, true);
  assert.equal(app.choice("选择颜色").disabled, true);
  assert.equal(app.addButton().disabled, true);
});

test("narrowing storage or model keeps the choices that still exist and drops the rest", () => {
  const app = phoneSelectionApp();
  // 只留 256GB：Pro 的 256GB 只有黑色，之前选的银色不再成立，被去掉。
  app.choice("选择容量").onChange(["256GB"]);
  assert.deepEqual(app.choice("选择颜色").options, [{ value: "黑色", label: "黑色" }]);
  assert.deepEqual(app.choice("选择颜色").values, []);
  assert.equal(app.addButton().disabled, true);
  app.choice("选择颜色").onChange(["黑色"]);
  assert.equal(app.addButton().disabled, false);
  // 换成 Pro Max：它没有 256GB，容量和颜色都跟着清空。
  app.choice("选择机型").onChange(["iphone18promax"]);
  assert.deepEqual(app.choice("选择容量").values, []);
  assert.deepEqual(app.choice("选择颜色").values, []);
  assert.equal(app.addButton().disabled, true);
});

test("switching model keeps a storage and colour the new model also has", () => {
  const app = phoneSelectionApp();
  // 从 Pro 换到 Pro Max，512GB 银色两款都有，不必重选。
  app.choice("选择机型").onChange(["iphone18promax"]);
  assert.deepEqual(app.choice("选择容量").values, ["512GB"]);
  assert.deepEqual(app.choice("选择颜色").values, ["银色"]);
  assert.equal(app.addButton().disabled, false);
});

test("several models, storages and colours add every existing combination for every store", async () => {
  const app = phoneSelectionApp();
  app.state.stores = [
    { number: "R532", title: "杭州万象城" },
    { number: "R471", title: "杭州西湖" },
  ];
  app.choice("选择自提门店").onChange(["R532", "R471"]);
  app.choice("选择机型").onChange(["iphone18pro", "iphone18promax"]);
  app.choice("选择容量").onChange(["256GB", "512GB"]);
  app.choice("选择颜色").onChange(["黑色", "银色"]);
  // 八种组合里目录只有三台，提示里点明规模。
  assert.equal(app.scaleHint().children.join(""), "3 个型号 × 2 家门店");
  assert.equal(app.addButton().disabled, false);
  app.addButton().onClick();
  await new Promise(setImmediate);
  assert.deepEqual(app.addedTargets, [[
    { locale: "zh_CN", storeNumber: "R532", storeTitle: "杭州万象城", partNumber: "A", productName: "iPhone 18 Pro 256GB 黑色" },
    { locale: "zh_CN", storeNumber: "R532", storeTitle: "杭州万象城", partNumber: "B", productName: "iPhone 18 Pro 512GB 银色" },
    { locale: "zh_CN", storeNumber: "R532", storeTitle: "杭州万象城", partNumber: "C", productName: "iPhone 18 Pro Max 512GB 银色" },
    { locale: "zh_CN", storeNumber: "R471", storeTitle: "杭州西湖", partNumber: "A", productName: "iPhone 18 Pro 256GB 黑色" },
    { locale: "zh_CN", storeNumber: "R471", storeTitle: "杭州西湖", partNumber: "B", productName: "iPhone 18 Pro 512GB 银色" },
    { locale: "zh_CN", storeNumber: "R471", storeTitle: "杭州西湖", partNumber: "C", productName: "iPhone 18 Pro Max 512GB 银色" },
  ]]);
  assert.deepEqual(app.choice("选择机型").values, []);
  assert.deepEqual(app.choice("选择自提门店").values, ["R532", "R471"]);
});

test("switching category or region clears the phone selection and preserves other categories", () => {
  const app = phoneSelectionApp();
  app.choice("选择品类").onChange("ipad");
  assert.equal(app.choice("选择型号").value, "");
  assert.deepEqual(app.choice("选择型号").options, [{ value: "D", label: "iPad Pro 256GB 银色" }]);
  assert.equal(app.addButton().disabled, true);
  app.choice("选择型号").onChange("D");
  assert.equal(app.addButton().disabled, false);
  app.choice("选择品类").onChange("iphone");
  assert.deepEqual(app.choice("选择机型").values, []);
  assert.equal(app.addButton().disabled, true);

  app.choice("选择机型").onChange(["iphone18pro"]);
  app.choice("选择容量").onChange(["512GB"]);
  app.choice("选择颜色").onChange(["银色"]);
  app.choice("选择地区").onChange("ja_JP");
  assert.deepEqual(app.choice("选择自提门店").values, []);
  assert.deepEqual(app.choice("选择机型").values, []);
  assert.deepEqual(app.choice("选择容量").values, []);
  assert.deepEqual(app.choice("选择颜色").values, []);
  assert.equal(app.addButton().disabled, true);
});

test("selecting several stores adds one target per store, skips existing ones, and keeps the stores selected", async () => {
  const app = phoneSelectionApp();
  app.state.stores = [
    { number: "R532", title: "杭州万象城" },
    { number: "R471", title: "杭州西湖" },
    { number: "R359", title: "上海南京东路" },
  ];
  // 上海南京东路的这一台已经在监控列表里了，再加不该重复。
  app.state.rows = [{
    target: { locale: "zh_CN", storeNumber: "R359", storeTitle: "上海南京东路", partNumber: "B", productName: "iPhone 18 Pro 512GB 银色" },
    availability: { kind: "out_of_stock" }, lastCheckedMs: null, consecutiveFailures: 0,
  }];
  app.choice("选择自提门店").onChange(["R532", "R471", "R359"]);
  assert.equal(app.addButton().disabled, false);
  app.addButton().onClick();
  await new Promise(setImmediate);
  assert.deepEqual(app.addedTargets, [[
    { locale: "zh_CN", storeNumber: "R359", storeTitle: "上海南京东路", partNumber: "B", productName: "iPhone 18 Pro 512GB 银色" },
    { locale: "zh_CN", storeNumber: "R532", storeTitle: "杭州万象城", partNumber: "B", productName: "iPhone 18 Pro 512GB 银色" },
    { locale: "zh_CN", storeNumber: "R471", storeTitle: "杭州西湖", partNumber: "B", productName: "iPhone 18 Pro 512GB 银色" },
  ]]);
  // 型号清掉、门店保留，接着给同一批门店加下一个型号最顺手。
  assert.deepEqual(app.choice("选择机型").values, []);
  assert.deepEqual(app.choice("选择自提门店").values, ["R532", "R471", "R359"]);
  assert.equal(app.addButton().disabled, true);
});

test("the add button stays disabled until at least one store is selected", () => {
  const app = phoneSelectionApp();
  app.choice("选择自提门店").onChange([]);
  assert.equal(app.addButton().disabled, true);
  app.choice("选择自提门店").onChange(["R532"]);
  assert.equal(app.addButton().disabled, false);
});

test("proxy addresses are one per row, saved as a list, and a row the backend drops is marked", () => {
  const app = renderApp();
  assert.deepEqual(app.proxyValues(), [""], "老设置没有 proxies 字段，应当是一个空行而不是报错");
  app.proxyRow(0).onChange({ target: { value: " socks5://127.0.0.1:1081 ; http://a:8080 " } });
  app.proxyRow(0).onBlur();
  assert.deepEqual(app.saved.at(-1).proxies, ["socks5://127.0.0.1:1081", "http://a:8080"]);
  assert.deepEqual(app.proxyValues(), ["socks5://127.0.0.1:1081", "http://a:8080"]);
  assert.equal(app.rejectedProxies(), 0);
  // 后端只留下能解析的地址：留不下的那一行要标出来，不能看着像生效了。
  app.addProxy().onClick();
  app.proxyRow(2).onChange({ target: { value: "ftp://x:21" } });
  app.proxyRow(2).onBlur();
  // 模拟后端把解析不了的那条丢掉。
  app.state.settings = { ...app.state.settings, proxies: ["socks5://127.0.0.1:1081", "http://a:8080"] };
  assert.equal(app.rejectedProxies(), 1);
  // 删掉它：剩下的两条和后端已存的一样，不必再写盘。
  const saves = app.saved.length;
  app.removeProxy(2).onClick();
  assert.deepEqual(app.proxyValues(), ["socks5://127.0.0.1:1081", "http://a:8080"]);
  assert.equal(app.rejectedProxies(), 0);
  assert.equal(app.saved.length, saves);
});

function row(target, availability) {
  return { target, availability, lastCheckedMs: null, consecutiveFailures: 0 };
}

test("the status line counts stock and untrusted rows and says when Apple's budget stretches the wait", () => {
  const app = renderApp();
  const target = (store) => ({ locale: "zh_CN", storeNumber: store, storeTitle: store, partNumber: "A", productName: "iPhone 18 Pro 256GB 黑色" });
  app.state.rows = [
    row(target("R1"), { kind: "in_stock" }),
    row(target("R2"), { kind: "out_of_stock" }),
    row(target("R3"), { kind: "unknown", reason: "blocked", detail: "HTTP 541" }),
    // 还没轮到、Apple 还没开放取货：都不算「查不到」。
    row(target("R4"), { kind: "unknown", reason: "not_yet_checked" }),
    row(target("R5"), { kind: "unknown", reason: "pickup_not_open", raw: "default" }),
  ];
  app.state.running = true;
  app.state.pacing = { nextCheckInSecs: 30, paced: false };
  assert.equal(app.status(), "每 30 秒查一轮 · 5 个目标 · 1 个有货 · 1 个查不到");
  app.state.pacing = { nextCheckInSecs: 85, paced: true };
  assert.equal(app.status(), "受 Apple 频率限制，下一轮 85 秒后 · 5 个目标 · 1 个有货 · 1 个查不到");
  app.state.running = false;
  assert.equal(app.status(), "5 个目标 · 1 个有货 · 1 个查不到");
});

test("the list groups targets by model with in-stock models first, and removing drops exactly those targets", async () => {
  const app = renderApp();
  const target = (store, part) => ({ locale: "zh_CN", storeNumber: store, storeTitle: `上海-${store}`, partNumber: part, productName: `型号 ${part}` });
  const a1 = target("R1", "A");
  const a2 = target("R2", "A");
  const b1 = target("R1", "B");
  // 先加的 A，后加的 B；快照和后端一样按「地区|门店|零件号」排。
  app.state.settings = { ...app.state.settings, targets: [a1, a2, b1] };
  app.state.rows = [row(a1, { kind: "out_of_stock" }), row(b1, { kind: "out_of_stock" }), row(a2, { kind: "out_of_stock" })];
  assert.deepEqual(app.watchList().groups.map((g) => g.key), ["zh_CN|A", "zh_CN|B"]);
  app.state.rows = [row(a1, { kind: "out_of_stock" }), row(b1, { kind: "in_stock" }), row(a2, { kind: "out_of_stock" })];
  assert.deepEqual(app.watchList().groups.map((g) => g.key), ["zh_CN|B", "zh_CN|A"]);
  // 删掉整组 A，B 原样留下。
  app.watchList().onRemove([a1, a2]);
  await new Promise(setImmediate);
  assert.deepEqual(app.addedTargets.at(-1), [b1]);
});

test("adding keeps the targets in the order they were added, not the snapshot's store order", async () => {
  const app = phoneSelectionApp();
  const watched = (store, part, name) => ({ locale: "zh_CN", storeNumber: store, storeTitle: store, partNumber: part, productName: name });
  const later = watched("R100", "A", "iPhone 18 Pro 256GB 黑色");
  const earlier = watched("R900", "C", "iPhone 18 Pro Max 512GB 银色");
  app.state.settings = { ...app.state.settings, targets: [earlier, later] };
  app.state.rows = [row(later, { kind: "out_of_stock" }), row(earlier, { kind: "out_of_stock" })];
  app.addButton().onClick();
  await new Promise(setImmediate);
  assert.deepEqual(app.addedTargets.at(-1).map((t) => `${t.storeNumber}|${t.partNumber}`), ["R900|C", "R100|A", "R532|B"]);
});

test("the add panel says how many of the picked combinations are already being watched", () => {
  const app = phoneSelectionApp();
  app.state.stores = [
    { number: "R532", title: "杭州万象城" },
    { number: "R471", title: "杭州西湖" },
  ];
  app.state.rows = [row({ locale: "zh_CN", storeNumber: "R532", storeTitle: "杭州万象城", partNumber: "B", productName: "iPhone 18 Pro 512GB 银色" }, { kind: "out_of_stock" })];
  app.choice("选择自提门店").onChange(["R532", "R471"]);
  assert.equal(app.scaleHint().children.join(""), "1 个型号 × 2 家门店，其中 1 个已在监控列表里");
  assert.equal(app.addButton().disabled, false);
  app.choice("选择自提门店").onChange(["R532"]);
  assert.equal(app.scaleHint().children.join(""), "1 个型号 × 1 家门店，都已在监控列表里");
  assert.equal(app.addButton().disabled, true, "全都在列表里时没有可加的，按钮不该亮着");
});
