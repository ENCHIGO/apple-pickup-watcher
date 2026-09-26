import assert from "node:assert/strict";
import test from "node:test";
import { loadSource } from "./load-source.mjs";

function renderApp() {
  const saved = [];
  const addedTargets = [];
  const pushSaves = [];
  const state = {
    settings: { locale: "zh_CN", barkUrl: "https://api.day.app/saved-key", intervalSeconds: 30 },
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
  const isPushInput = (element) => /^push-\d+$/.test(element?.props?.id ?? "");
  const isAddPush = (element) => element.type === "Button" &&
    Array.isArray(element.props.children) && element.props.children.includes(" 添加推送地址");
  return {
    proxies: () => control((element) => element?.props?.id === "proxies"),
    pushRow: (index) => control((element) => element?.props?.id === `push-${index}`),
    pushValues: () => all(isPushInput).map((element) => element.props.value),
    // 渠道标签按行的顺序排列；还没保存的行没有标签。
    pushLabels: () => all((element) => element.type === "Badge").map((element) => element.props.children),
    removePush: (index) => control((element) => element?.props?.["aria-label"] === `删除推送地址 ${index + 1}`),
    addPush: () => control(isAddPush),
    hasAddPush: () => all(isAddPush).length > 0,
    choice: (placeholder) => control((element) => element?.props?.placeholder === placeholder),
    addButton: () => control((element) => element.type === "Button" &&
      Array.isArray(element.props.children) && element.props.children.includes(" 添加")),
    scaleHint: () => control((element) => element.type === "span" &&
      Array.isArray(element.props.children) && element.props.children.includes(" 个型号 × ")),
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
  app.state.settings = { locale: "zh_CN", barkUrl: "", intervalSeconds: 30 };
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

test("proxy addresses are split on semicolons and saved as a list, and old settings without the field read as empty", () => {
  const app = renderApp();
  assert.equal(app.proxies().value, "", "老设置没有 proxies 字段，输入框应当是空的而不是报错");
  app.proxies().onChange({ target: { value: " http://a:8080 ; socks5://b:1080  " } });
  assert.equal(app.proxies().value, " http://a:8080 ; socks5://b:1080  ");
  app.proxies().onBlur();
  assert.deepEqual(app.saved.at(-1).proxies, ["http://a:8080", "socks5://b:1080"]);
  app.state.settings = { ...app.state.settings, proxies: ["http://c:1"] };
  assert.equal(app.proxies().value, "http://c:1", "没在编辑时跟随后端保存的列表");
});
