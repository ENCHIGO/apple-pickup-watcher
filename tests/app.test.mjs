import assert from "node:assert/strict";
import test from "node:test";
import { loadSource } from "./load-source.mjs";

function renderApp() {
  const saved = [];
  const state = {
    settings: { barkUrl: "https://api.day.app/saved-key", intervalSeconds: 30 },
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
    };
    if (specifier.startsWith("@/components/") || specifier === "lucide-react") {
      return new Proxy({}, { get: (_target, name) => String(name) });
    }
  }).default;
  function input() {
    cursor = 0;
    const find = (element) => {
      if (!element || typeof element !== "object") return;
      if (element?.props?.id === "bark") return element;
      const children = element?.props?.children;
      for (const child of Array.isArray(children) ? children : [children]) {
        const result = find(child);
        if (result) return result;
      }
    };
    return find(App()).props;
  }
  return { input, state, saved };
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
