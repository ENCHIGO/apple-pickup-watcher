import {
  describeAvailability,
  type StatusTone,
  type Store,
  type Target,
  type TargetState,
  targetKey,
} from "@/lib/types";

/** 监控列表里的一组：同一个地区的同一个型号，在各家门店的状态。 */
export interface ProductGroup {
  /** 「地区|零件号」。不同地区的同一机型零件号不同，自然分成两组。 */
  key: string;
  locale: string;
  productName: string;
  rows: TargetState[];
  inStock: boolean;
}

function productKey(target: Target): string {
  return `${target.locale}|${target.partNumber}`;
}

/**
 * 把快照按型号分组。
 *
 * 快照本身按「地区|门店|零件号」排序，组内的门店因此按门店号排，各组一致。
 * 组的先后按加入监控的顺序（`order` 是设置里的目标列表），有货的组提到最前：
 * 这一刻是程序存在的全部理由，不能让它落在列表下面要滚动才看得到。其余组
 * 不随每轮结果挪位置，列表不会每 30 秒跳一次。
 */
export function groupByProduct(rows: TargetState[], order: Target[]): ProductGroup[] {
  const groups = new Map<string, ProductGroup>();
  for (const row of rows) {
    const key = productKey(row.target);
    let group = groups.get(key);
    if (group === undefined) {
      group = {
        key,
        locale: row.target.locale,
        productName: row.target.productName,
        rows: [],
        inStock: false,
      };
      groups.set(key, group);
    }
    group.rows.push(row);
    if (row.availability.kind === "in_stock") group.inStock = true;
  }

  const added = new Map<string, number>();
  order.forEach((target, index) => {
    const key = productKey(target);
    if (!added.has(key)) added.set(key, index);
  });
  const rank = (group: ProductGroup) => added.get(group.key) ?? order.length;
  const all = [...groups.values()].sort((a, b) => rank(a) - rank(b));
  return [...all.filter((g) => g.inStock), ...all.filter((g) => !g.inStock)];
}

/**
 * 快照里的目标，按加入监控的顺序排。
 *
 * 快照按「地区|门店|零件号」排序，拿它原样存回设置，用户加入的先后就丢了，
 * 列表里各组的位置会在下一次添加或删除之后变掉。有哪些目标以快照为准（前端
 * 不持有真源），先后取设置里的目标列表；设置里没有的排在最后，保持快照顺序。
 */
export function inAddedOrder(rows: TargetState[], order: Target[]): Target[] {
  const index = new Map<string, number>();
  order.forEach((target, i) => {
    const key = targetKey(target);
    if (!index.has(key)) index.set(key, i);
  });
  const rank = (target: Target) => index.get(targetKey(target)) ?? order.length;
  return rows.map((row) => row.target).sort((a, b) => rank(a) - rank(b));
}

export interface StatusCount {
  label: string;
  tone: StatusTone;
  count: number;
}

const TONE_RANK: Record<StatusTone, number> = { inStock: 0, unknown: 1, pending: 2, outOfStock: 3 };

/**
 * 一组的状态计数，例如「2 家有货 · 1 家查不到 · 5 家无货」，要紧的排前面。
 *
 * 状态文字取自 `describeAvailability`，不另起一套；只有「未知」在计数里说成
 * 「查不到」，和顶栏的「N 个查不到」一致。「待查询」和「未开放取货」同属弱化
 * 的一档，但文字不同，各算各的。
 */
export function summarize(rows: TargetState[]): StatusCount[] {
  const counts = new Map<string, StatusCount>();
  for (const row of rows) {
    const { label, tone } = describeAvailability(row.availability);
    const shown = tone === "unknown" ? "查不到" : label;
    const entry = counts.get(shown) ?? { label: shown, tone, count: 0 };
    entry.count += 1;
    counts.set(shown, entry);
  }
  // 稳定排序：同一档里保持出现的先后。
  return [...counts.values()].sort((a, b) => TONE_RANK[a.tone] - TONE_RANK[b.tone]);
}

/**
 * 把门店标题拆成「地区」和「店名」：标题是「上海-南京东路」「Tokyo-Shibuya」这样的。
 *
 * 按目录里的店名切，不按连字符切：地区名自己也可能带连字符
 * （「Baden-Württemberg-Sindelfingen」的店名是「Sindelfingen」）。目录里查不到
 * 这家店，或者标题和店名对不上，就不拆，整个标题当店名。
 */
export function splitStoreTitle(title: string, store: Store | undefined): { area: string; name: string } {
  if (store !== undefined && store.name !== "" && title.endsWith(`-${store.name}`)) {
    return { area: title.slice(0, -store.name.length - 1), name: store.name };
  }
  return { area: "", name: title };
}

export interface StoreArea {
  /** 同一组里共用的地区名；拆不出来时为空串。 */
  area: string;
  stores: { row: TargetState; name: string }[];
}

/** 一组里的门店按地区归拢，地区名只写一次。顺序沿用快照里的门店顺序。 */
export function storeAreas(rows: TargetState[], stores: ReadonlyMap<string, Store>): StoreArea[] {
  const areas: StoreArea[] = [];
  for (const row of rows) {
    const { area, name } = splitStoreTitle(row.target.storeTitle, stores.get(row.target.storeNumber));
    let bucket = areas.find((a) => a.area === area);
    if (bucket === undefined) {
      bucket = { area, stores: [] };
      areas.push(bucket);
    }
    bucket.stores.push({ row, name });
  }
  return areas;
}

/** 最近一次查询的时间；一条都还没查过时为 null。 */
export function latestCheck(rows: TargetState[]): number | null {
  let latest: number | null = null;
  for (const row of rows) {
    if (row.lastCheckedMs !== null && (latest === null || row.lastCheckedMs > latest)) {
      latest = row.lastCheckedMs;
    }
  }
  return latest;
}
