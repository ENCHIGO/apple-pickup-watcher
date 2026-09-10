import assert from "node:assert/strict";
import test from "node:test";
import { loadSource } from "./load-source.mjs";

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

function catalog(locale, revision = "initial") {
  return {
    stores: [{ number: `${locale}-store`, name: locale, title: `${locale} store` }],
    products: [{ partNumber: `${locale}-${revision}`, title: revision, category: "iphone" }],
  };
}

function setup() {
  const calls = [];
  const handlers = {};
  const store = loadSource("src/lib/store.ts", (specifier) => {
    if (specifier === "@tauri-apps/api/core") return {
      invoke: async (command, args) => {
        calls.push({ command, args });
        if (handlers[command]) return handlers[command](args);
        if (command === "save_settings") return args.settings;
        if (command === "list_stores") return catalog(args.locale).stores;
        if (command === "list_products") return catalog(args.locale).products;
        throw new Error(`Unexpected command: ${command}`);
      },
    };
    if (specifier === "@tauri-apps/api/event") return {};
  });
  return { ...store, snapshot: store.watcherStore.getSnapshot, calls, handlers };
}

test("a completed refresh for the previous locale cannot replace the selected catalog", async () => {
  const app = setup();
  const refresh = deferred();
  app.handlers.refresh_products = () => refresh.promise;
  await app.loadCatalog("zh_CN");
  const pending = app.refreshProducts();
  await app.changeLocale("ja_JP");
  const count = app.calls.length;
  refresh.resolve(12);
  await pending;
  assert.equal(app.snapshot().settings.locale, "ja_JP");
  assert.deepEqual(app.snapshot().products, catalog("ja_JP").products);
  assert.equal(app.calls.length, count, "no obsolete catalog request should be started");
  assert.equal(app.snapshot().refreshing, false);
});

test("a pending catalog cannot repopulate its old locale after switching", async () => {
  const app = setup();
  const oldProducts = deferred();
  app.handlers.list_products = ({ locale }) =>
    locale === "zh_CN" ? oldProducts.promise : catalog(locale).products;
  const pending = app.loadCatalog("zh_CN");
  await app.changeLocale("ja_JP");
  oldProducts.resolve(catalog("zh_CN").products);
  await pending;
  assert.deepEqual(app.snapshot().stores, catalog("ja_JP").stores);
  assert.deepEqual(app.snapshot().products, catalog("ja_JP").products);
});

test("the newest request wins when same-locale catalog requests finish out of order", async () => {
  const app = setup();
  const older = deferred();
  app.handlers.list_products = () => older.promise;
  const pending = app.loadCatalog("zh_CN");
  app.handlers.list_products = () => catalog("zh_CN", "new").products;
  await app.loadCatalog("zh_CN");
  older.resolve(catalog("zh_CN", "old").products);
  await pending;
  assert.deepEqual(app.snapshot().products, catalog("zh_CN", "new").products);
});

test("a stale request failure cannot clear a newer catalog or log a current failure", async () => {
  const app = setup();
  const older = deferred();
  app.handlers.list_products = () => older.promise;
  const pending = app.loadCatalog("zh_CN");
  app.handlers.list_products = () => catalog("zh_CN", "new").products;
  await app.loadCatalog("zh_CN");
  older.reject(new Error("obsolete failure"));
  await pending;
  assert.deepEqual(app.snapshot().products, catalog("zh_CN", "new").products);
  assert.equal(app.snapshot().logs.length, 0);
});

test("switching locale immediately clears old choices while the new catalog loads", async () => {
  const app = setup();
  await app.loadCatalog("zh_CN");
  const loading = deferred();
  const requested = deferred();
  app.handlers.list_products = () => { requested.resolve(); return loading.promise; };
  const pending = app.changeLocale("ja_JP");
  await requested.promise;
  assert.equal(app.snapshot().settings.locale, "ja_JP");
  assert.deepEqual(app.snapshot().stores, []);
  assert.deepEqual(app.snapshot().products, []);
  loading.resolve(catalog("ja_JP").products);
  await pending;
  assert.deepEqual(app.snapshot().products, catalog("ja_JP").products);
});

test("a failed locale save keeps the original locale and catalog without loading new choices", async () => {
  const app = setup();
  await app.loadCatalog("zh_CN");
  const count = app.calls.length;
  app.handlers.save_settings = () => { throw new Error("disk full"); };
  await app.changeLocale("ja_JP");
  assert.equal(app.snapshot().settings.locale, "zh_CN");
  assert.deepEqual(app.snapshot().products, catalog("zh_CN").products);
  assert.deepEqual(app.calls.slice(count).map((call) => call.command), ["save_settings"]);
  assert.match(app.snapshot().logs.at(-1), /保存设置失败.*disk full/);
});

test("a delayed settings save that restores the old locale also restores its matching catalog", async () => {
  const app = setup();
  await app.loadCatalog("zh_CN");
  const previousSettings = app.snapshot().settings;
  const localeSave = deferred();
  const soundSave = deferred();
  app.handlers.save_settings = ({ settings }) =>
    settings.locale === "ja_JP" ? localeSave.promise : soundSave.promise;

  const switching = app.changeLocale("ja_JP");
  const soundSettings = { ...previousSettings, soundEnabled: false };
  const togglingSound = app.saveSettings(soundSettings);
  localeSave.resolve({ ...previousSettings, locale: "ja_JP" });
  await switching;
  assert.deepEqual(app.snapshot().products, catalog("ja_JP").products);

  soundSave.resolve(soundSettings);
  await togglingSound;
  assert.equal(app.snapshot().settings.locale, "zh_CN");
  assert.equal(app.snapshot().settings.soundEnabled, false);
  assert.deepEqual(app.snapshot().stores, catalog("zh_CN").stores);
  assert.deepEqual(app.snapshot().products, catalog("zh_CN").products);
});

test("a current catalog failure clears choices and reports its cause", async () => {
  const app = setup();
  await app.loadCatalog("zh_CN");
  app.handlers.list_products = () => { throw new Error("unreadable catalog"); };
  await app.loadCatalog("zh_CN");
  assert.deepEqual(app.snapshot().stores, []);
  assert.deepEqual(app.snapshot().products, []);
  assert.match(app.snapshot().logs.at(-1), /载入 zh_CN.*unreadable catalog/);
});

test("a failed refresh still reloads the current catalog to expose partial backend results", async () => {
  const app = setup();
  app.handlers.refresh_products = () => { throw new Error("one page failed"); };
  app.handlers.list_products = () => catalog("zh_CN", "partial").products;
  await app.refreshProducts();
  assert.deepEqual(app.snapshot().products, catalog("zh_CN", "partial").products);
  assert.equal(app.snapshot().refreshing, false);
  assert.match(app.snapshot().logs.at(-1), /更新型号列表失败.*one page failed/);
  assert.match(app.snapshot().logs.at(-1), /旧目录.*可能已过期/);
});
