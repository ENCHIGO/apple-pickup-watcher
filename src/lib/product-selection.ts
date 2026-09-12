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

export function capacityOptions(
  products: Product[],
  category: Category,
  family: string,
): ProductOption[] {
  return uniqueOptions(
    products
      .filter(
        (product) =>
          product.category === category && product.family === family && product.capacity !== "",
      )
      .map((product) => ({ value: product.capacity, label: product.capacity })),
  );
}

export function colorOptions(
  products: Product[],
  category: Category,
  family: string,
  capacity: string,
): ProductOption[] {
  return uniqueOptions(
    products
      .filter(
        (product) =>
          product.category === category &&
          product.family === family &&
          product.capacity === capacity &&
          product.color !== "",
      )
      .map((product) => ({ value: product.color, label: product.color })),
  );
}

export function productForSelection(
  products: Product[],
  category: Category,
  family: string,
  capacity: string,
  color: string,
): Product | undefined {
  return products.find(
    (product) =>
      product.category === category &&
      product.family === family &&
      product.capacity === capacity &&
      product.color === color,
  );
}
