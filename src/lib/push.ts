import type { Settings } from "@/lib/types";

/**
 * 推送渠道，与 Rust 侧 `config::PushKind` 一一对应。
 *
 * 界面上 Bark、飞书和 Telegram 是同一个「推送地址」列表，用户不用自己选渠道。
 * 哪个地址归哪个渠道由后端按地址长相判断（`Settings::set_push_urls`），这里只按
 * 后端存放的位置读出渠道，不另抄一份判断规则：抄一份，迟早会有一边先认了新渠道、
 * 另一边还按老规矩归类。
 */
export type PushKind = "bark" | "feishu" | "telegram";

export interface PushAddress {
  url: string;
  kind: PushKind;
}

export const PUSH_KIND_LABEL: Record<PushKind, string> = {
  bark: "Bark",
  feishu: "飞书",
  telegram: "Telegram",
};

/** 每个渠道的地址存在设置的哪个字段里；键的顺序与后端 `push_urls` 一致。 */
const PUSH_FIELD: Record<PushKind, "barkUrl" | "feishuWebhook" | "telegramUrl"> = {
  bark: "barkUrl",
  feishu: "feishuWebhook",
  telegram: "telegramUrl",
};

const PUSH_KINDS = Object.keys(PUSH_FIELD) as PushKind[];

/** 文件里每个渠道的地址是分号连起来的规范写法，后端保存时已去空、去重。 */
function splitStored(raw: string | undefined): string[] {
  return (raw ?? "")
    .split(";")
    .map((url) => url.trim())
    .filter((url) => url !== "");
}

/**
 * 已保存的推送地址，摊成一张列表：依次是 Bark、飞书、Telegram，与后端 `push_urls`
 * 的顺序一致。旧设置文件没有 feishuWebhook、telegramUrl 字段，读上来是 undefined，
 * 当作空。
 */
export function savedPushAddresses(settings: Settings): PushAddress[] {
  return PUSH_KINDS.flatMap((kind) =>
    splitStored(settings[PUSH_FIELD[kind]]).map((url) => ({ url, kind })),
  );
}

/**
 * 把编辑中的几行整理成要保存的地址：去掉首尾空白和空行；一行里粘了好几个地址
 * （老版本用分号连着的写法）就拆开；重复的只留第一个。
 */
export function cleanPushRows(rows: string[]): string[] {
  const out: string[] = [];
  for (const row of rows) {
    for (const piece of row.split(/[;\s]+/)) {
      const url = piece.trim();
      if (url !== "" && !out.includes(url)) out.push(url);
    }
  }
  return out;
}

/** 两组地址是否相同，不计顺序：只是顺序变了，不必重写设置文件。 */
export function samePushUrls(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((url) => b.includes(url));
}

/** 设置收起时摘要里的那一段，例如「推送：Bark 2 个、飞书 1 个」。 */
export function pushSummary(addresses: PushAddress[]): string {
  if (addresses.length === 0) return "推送未配置";
  const parts = PUSH_KINDS
    .map((kind) => [kind, addresses.filter((a) => a.kind === kind).length] as const)
    .filter(([, count]) => count > 0)
    .map(([kind, count]) => `${PUSH_KIND_LABEL[kind]} ${count} 个`);
  return `推送：${parts.join("、")}`;
}
