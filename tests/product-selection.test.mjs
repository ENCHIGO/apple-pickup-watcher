import assert from "node:assert/strict";
import test from "node:test";
import { loadSource } from "./load-source.mjs";

const selection = loadSource("src/lib/product-selection.ts");

const products = [
  {
    partNumber: "A",
    category: "iphone",
    family: "iphone18pro",
    capacity: "256GB",
    color: "黑色",
    title: "iPhone 18 Pro 256GB 黑色",
  },
  {
    partNumber: "B",
    category: "iphone",
    family: "iphone18pro",
    capacity: "512GB",
    color: "银色",
    title: "iPhone 18 Pro 512GB 银色",
  },
  {
    partNumber: "C",
    category: "iphone",
    family: "iphone18promax",
    capacity: "512GB",
    color: "银色",
    title: "iPhone 18 Pro Max 512GB 银色",
  },
];

test("iPhone selection exposes model, storage and colour separately", () => {
  assert.deepEqual(selection.familyOptions(products, "iphone"), [
    { value: "iphone18pro", label: "iPhone 18 Pro" },
    { value: "iphone18promax", label: "iPhone 18 Pro Max" },
  ]);
  assert.deepEqual(selection.capacityOptions(products, "iphone", ["iphone18pro"]), [
    { value: "256GB", label: "256GB" },
    { value: "512GB", label: "512GB" },
  ]);
  assert.deepEqual(selection.colorOptions(products, "iphone", ["iphone18pro"], ["512GB"]), [
    { value: "银色", label: "银色" },
  ]);
});

test("storage and colour options are the union over every selected model and storage", () => {
  assert.deepEqual(
    selection.capacityOptions(products, "iphone", ["iphone18pro", "iphone18promax"]),
    [
      { value: "256GB", label: "256GB" },
      { value: "512GB", label: "512GB" },
    ],
  );
  assert.deepEqual(
    selection.colorOptions(
      products,
      "iphone",
      ["iphone18pro", "iphone18promax"],
      ["256GB", "512GB"],
    ),
    [
      { value: "黑色", label: "黑色" },
      { value: "银色", label: "银色" },
    ],
  );
  assert.deepEqual(selection.capacityOptions(products, "iphone", []), []);
});

test("a selection resolves to every existing combination and skips ones the catalog lacks", () => {
  assert.deepEqual(
    selection
      .productsForSelection(
        products,
        "iphone",
        ["iphone18pro", "iphone18promax"],
        ["512GB"],
        ["银色"],
      )
      .map((p) => p.partNumber),
    ["B", "C"],
  );
  // Pro 的 256GB 没有银色：这个组合落空，既不报错也不凑合成别的型号。
  assert.deepEqual(
    selection.productsForSelection(products, "iphone", ["iphone18pro"], ["256GB"], ["银色"]),
    [],
  );
});

test("narrowing the parent choice keeps only the child choices that still exist", () => {
  const options = selection.capacityOptions(products, "iphone", ["iphone18promax"]);
  assert.deepEqual(selection.keepAvailable(["256GB", "512GB"], options), ["512GB"]);
  assert.deepEqual(selection.keepAvailable([], options), []);
});
