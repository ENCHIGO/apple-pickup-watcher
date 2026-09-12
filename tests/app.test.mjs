import assert from "node:assert/strict";
import test from "node:test";
import { loadSource } from "./load-source.mjs";

function renderApp() {
  const saved = [];
  const addedTargets = [];
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
  return {
    input: () => control((element) => element?.props?.id === "bark"),
    choice: (placeholder) => control((element) => element?.props?.placeholder === placeholder),
    addButton: () => control((element) => element.type === "Button" &&
      Array.isArray(element.props.children) && element.props.children.includes(" 添加")),
    state, saved, addedTargets,
  };
}

test("clearing a saved Bark URL keeps the input empty and persists an empty URL on blur", () => {
  const app = renderApp();
  assert.equal(app.input().value, "https://api.day.app/saved-key");
  app.input().onChange({ target: { value: "" } });
  assert.equal(app.input().value, "");
  app.input().onBlur();
  assert.equal(app.saved.at(-1).barkUrl, "");
  assert.equal(app.input().value, "");
});

test("an untouched Bark field follows backend settings and a saved edit is trimmed", () => {
  const app = renderApp();
  app.input();
  app.state.settings = { ...app.state.settings, barkUrl: "https://api.day.app/loaded-key" };
  assert.equal(app.input().value, "https://api.day.app/loaded-key");
  app.input().onChange({ target: { value: " https://api.day.app/new-key  " } });
  app.input().onBlur();
  assert.equal(app.saved.at(-1).barkUrl, "https://api.day.app/new-key");
  assert.equal(app.input().value, "https://api.day.app/new-key");
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
  app.choice("选择自提门店").onChange("R532");
  app.choice("选择机型").onChange("iphone18pro");
  app.choice("选择容量").onChange("512GB");
  app.choice("选择颜色").onChange("银色");
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
  assert.equal(app.choice("选择机型").value, "");
  assert.equal(app.choice("选择容量").disabled, true);
  assert.equal(app.choice("选择颜色").disabled, true);
  assert.equal(app.addButton().disabled, true);
});

test("changing storage or model clears the previous SKU before adding a target", () => {
  const app = phoneSelectionApp();
  app.choice("选择容量").onChange("256GB");
  assert.equal(app.choice("选择颜色").value, "");
  assert.deepEqual(app.choice("选择颜色").options, [{ value: "黑色", label: "黑色" }]);
  assert.equal(app.addButton().disabled, true);
  app.choice("选择颜色").onChange("黑色");
  assert.equal(app.addButton().disabled, false);
  app.choice("选择机型").onChange("iphone18promax");
  assert.equal(app.choice("选择容量").value, "");
  assert.equal(app.choice("选择颜色").value, "");
  assert.equal(app.addButton().disabled, true);
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
  assert.equal(app.choice("选择机型").value, "");
  assert.equal(app.addButton().disabled, true);

  app.choice("选择机型").onChange("iphone18pro");
  app.choice("选择容量").onChange("512GB");
  app.choice("选择颜色").onChange("银色");
  app.choice("选择地区").onChange("ja_JP");
  assert.equal(app.choice("选择自提门店").value, "");
  assert.equal(app.choice("选择机型").value, "");
  assert.equal(app.choice("选择容量").value, "");
  assert.equal(app.choice("选择颜色").value, "");
  assert.equal(app.addButton().disabled, true);
});
