import type { Category, Product } from "@/lib/types";

export interface ProductOption {
  value: string;
  label: string;
}

function uniqueOptions(items: ProductOption[]): ProductOption[] {
  const seen = new Set<string>();
  return items.filter((item) => {
    if (seen.has(item.value)) return false;
    seen.add(item.value);
    return true;
  });
}

function familyLabel(product: Product): string {
  let label = product.title;
  for (const detail of [product.capacity, product.color]) {
    if (detail) label = label.replace(detail, "");
  }
  return label.replace(/\s+/g, " ").trim() || product.family;
}

export function familyOptions(products: Product[], category: Category): ProductOption[] {
  return uniqueOptions(
    products
      .filter((product) => product.category === category)
      .map((product) => ({ value: product.family, label: familyLabel(product) })),
  );
}

/** 所选机型（可多个）下出现过的容量，按目录顺序去重。 */
export function capacityOptions(
  products: Product[],
  category: Category,
  families: string[],
): ProductOption[] {
  return uniqueOptions(
    products
      .filter(
        (product) =>
          product.category === category &&
          families.includes(product.family) &&
          product.capacity !== "",
      )
      .map((product) => ({ value: product.capacity, label: product.capacity })),
  );
}

/** 所选机型与容量的组合下出现过的颜色，按目录顺序去重。 */
export function colorOptions(
  products: Product[],
  category: Category,
  families: string[],
  capacities: string[],
): ProductOption[] {
  return uniqueOptions(
    products
      .filter(
        (product) =>
          product.category === category &&
          families.includes(product.family) &&
          capacities.includes(product.capacity) &&
          product.color !== "",
      )
      .map((product) => ({ value: product.color, label: product.color })),
  );
}

/**
 * 三级选择的组合里目录中真实存在的型号，按目录顺序。
 *
 * 三级都是多选，组合是笛卡尔积；目录里没有的组合（例如某个颜色只有 Pro Max
 * 才有）自然落空，用户不需要知道哪些组合不存在。
 */
export function productsForSelection(
  products: Product[],
  category: Category,
  families: string[],
  capacities: string[],
  colors: string[],
): Product[] {
  return products.filter(
    (product) =>
      product.category === category &&
      families.includes(product.family) &&
      capacities.includes(product.capacity) &&
      colors.includes(product.color),
  );
}

/**
 * 上级选择变了以后，下级只保留仍然可选的项。
 *
 * 从 Pro 换到 Pro Max，用户要的多半还是那个「256GB 黑色」，全清掉让人重选一遍
 * 是多余的；但只有 Pro 才有的容量或颜色必须去掉，否则会留下一个永远匹配不到
 * 型号的幽灵选项，「添加」按钮亮着却什么都加不进去。
 */
export function keepAvailable(values: string[], options: ProductOption[]): string[] {
  return values.filter((value) => options.some((option) => option.value === value));
}
