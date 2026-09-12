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

test("iPhone selection exposes model, storage and color separately", () => {
  assert.deepEqual(selection.familyOptions(products, "iphone"), [
    { value: "iphone18pro", label: "iPhone 18 Pro" },
    { value: "iphone18promax", label: "iPhone 18 Pro Max" },
  ]);
  assert.deepEqual(selection.capacityOptions(products, "iphone", "iphone18pro"), [
    { value: "256GB", label: "256GB" },
    { value: "512GB", label: "512GB" },
  ]);
  assert.deepEqual(selection.colorOptions(products, "iphone", "iphone18pro", "512GB"), [
    { value: "银色", label: "银色" },
  ]);
});

test("an exact selection resolves to one Apple part number", () => {
  assert.equal(
    selection.productForSelection(
      products,
      "iphone",
      "iphone18promax",
      "512GB",
      "银色",
    ).partNumber,
    "C",
  );
});
