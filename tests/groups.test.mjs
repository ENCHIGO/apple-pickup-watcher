import assert from "node:assert/strict";
import test from "node:test";
import { loadSource } from "./load-source.mjs";

const groups = loadSource("src/lib/groups.ts");

const OUT = { kind: "out_of_stock" };
const IN = { kind: "in_stock" };

function target(store, part, extra = {}) {
  return {
    locale: "zh_CN",
    storeNumber: store,
    storeTitle: `上海-${store}`,
    partNumber: part,
    productName: `iPhone ${part}`,
    ...extra,
  };
}

function row(t, availability, lastCheckedMs = null) {
  return { target: t, availability, lastCheckedMs, consecutiveFailures: 0 };
}

test("targets are grouped by model in the order they were added, and a model in stock moves to the top", () => {
  const duo1 = target("R359", "DUO");
  const duo2 = target("R678", "DUO");
  const pro1 = target("R359", "PRO");
  const pro2 = target("R678", "PRO");
  // 快照和后端一样按「地区|门店|零件号」排，组内门店因此按门店号排。
  const rows = [row(duo1, OUT), row(pro1, OUT), row(duo2, OUT), row(pro2, OUT)];
  const order = [duo1, duo2, pro1, pro2];
  const shape = (list) => list.map((g) => [g.key, g.inStock, g.rows.map((r) => r.target.storeNumber)]);
  assert.deepEqual(shape(groups.groupByProduct(rows, order)), [
    ["zh_CN|DUO", false, ["R359", "R678"]],
    ["zh_CN|PRO", false, ["R359", "R678"]],
  ]);
  rows[3] = row(pro2, IN);
  assert.deepEqual(shape(groups.groupByProduct(rows, order)), [
    ["zh_CN|PRO", true, ["R359", "R678"]],
    ["zh_CN|DUO", false, ["R359", "R678"]],
  ]);
});

test("the same part number in two regions stays in two groups", () => {
  const cn = target("R359", "A");
  const hk = target("R409", "A", { locale: "zh_HK" });
  const list = groups.groupByProduct([row(cn, OUT), row(hk, OUT)], [hk, cn]);
  assert.deepEqual(list.map((g) => [g.key, g.locale]), [["zh_HK|A", "zh_HK"], ["zh_CN|A", "zh_CN"]]);
});

test("targets come back in the order they were added, not the snapshot's store order", () => {
  const first = target("R678", "A");
  const second = target("R359", "A");
  const third = target("R359", "B");
  const stray = target("R001", "C");
  // 快照按门店号排；stray 不在设置里（例如设置还没来得及更新），排到最后。
  const rows = [row(stray, OUT), row(second, OUT), row(third, OUT), row(first, OUT)];
  assert.deepEqual(groups.inAddedOrder(rows, [first, second, third]), [first, second, third, stray]);
});

test("a group's counts put what matters first and call unknown 查不到", () => {
  const t = target("R1", "A");
  const rows = [
    row(t, OUT),
    row(t, OUT),
    row(t, { kind: "unknown", reason: "blocked", detail: "HTTP 541" }),
    row(t, { kind: "unknown", reason: "transport", detail: "timeout" }),
    row(t, { kind: "unknown", reason: "not_yet_checked" }),
    row(t, { kind: "unknown", reason: "pickup_not_open", raw: "default" }),
    row(t, IN),
  ];
  // 「待查询」和「未开放取货」同属弱化的一档，但不能混成一个数。
  assert.deepEqual(
    groups.summarize(rows).map((s) => `${s.count} 家${s.label}`),
    ["1 家有货", "2 家查不到", "1 家待查询", "1 家未开放取货", "2 家无货"],
  );
});

test("store titles split into area and name by the catalog name, not by the first hyphen", () => {
  assert.deepEqual(
    groups.splitStoreTitle("上海-南京东路", { number: "R359", name: "南京东路", title: "上海-南京东路" }),
    { area: "上海", name: "南京东路" },
  );
  assert.deepEqual(
    groups.splitStoreTitle("Baden-Württemberg-Sindelfingen", {
      number: "R1",
      name: "Sindelfingen",
      title: "Baden-Württemberg-Sindelfingen",
    }),
    { area: "Baden-Württemberg", name: "Sindelfingen" },
  );
  // 当前目录里没有这家店（例如另一个地区的），或者标题和店名对不上：整个标题当店名。
  assert.deepEqual(groups.splitStoreTitle("Tokyo-Shibuya", undefined), { area: "", name: "Tokyo-Shibuya" });
  assert.deepEqual(
    groups.splitStoreTitle("上海-南京东路", { number: "R359", name: "静安", title: "上海-静安" }),
    { area: "", name: "上海-南京东路" },
  );
});

test("stores in a group are listed under their area once, keeping the snapshot order", () => {
  const stores = new Map([
    ["R320", { number: "R320", name: "三里屯", title: "北京-三里屯" }],
    ["R359", { number: "R359", name: "南京东路", title: "上海-南京东路" }],
    ["R678", { number: "R678", name: "静安", title: "上海-静安" }],
  ]);
  const rows = [
    row(target("R320", "A", { storeTitle: "北京-三里屯" }), OUT),
    row(target("R359", "A", { storeTitle: "上海-南京东路" }), OUT),
    row(target("R678", "A", { storeTitle: "上海-静安" }), OUT),
    row(target("R710", "A", { storeTitle: "Tokyo-Shibuya" }), OUT),
  ];
  assert.deepEqual(
    groups.storeAreas(rows, stores).map((a) => [a.area, a.stores.map((s) => s.name)]),
    [["北京", ["三里屯"]], ["上海", ["南京东路", "静安"]], ["", ["Tokyo-Shibuya"]]],
  );
});

test("the last check is the newest time any row was checked", () => {
  const t = target("R1", "A");
  assert.equal(groups.latestCheck([]), null);
  assert.equal(groups.latestCheck([row(t, OUT, null)]), null);
  assert.equal(groups.latestCheck([row(t, OUT, 1000), row(t, OUT, null), row(t, OUT, 3000), row(t, OUT, 2000)]), 3000);
});
