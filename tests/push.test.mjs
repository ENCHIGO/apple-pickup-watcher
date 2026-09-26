import assert from "node:assert/strict";
import test from "node:test";
import { loadSource } from "./load-source.mjs";

const push = loadSource("src/lib/push.ts");

const K1 = "https://api.day.app/k1";
const K2 = "https://api.day.app/k2";
const F1 = "https://open.feishu.cn/open-apis/bot/v2/hook/f1";

test("saved addresses are read back Bark first, and a missing Feishu field reads as empty", () => {
  assert.deepEqual(push.savedPushAddresses({ barkUrl: `${K1};${K2}`, feishuWebhook: F1 }), [
    { url: K1, kind: "bark" },
    { url: K2, kind: "bark" },
    { url: F1, kind: "feishu" },
  ]);
  assert.deepEqual(push.savedPushAddresses({ barkUrl: "" }), []);
});

test("rows are trimmed, split on semicolons and whitespace, and de-duplicated in order", () => {
  assert.deepEqual(push.cleanPushRows([` ${K1} `, "", `${F1};${K1}`, `${K2}\n`]), [K1, F1, K2]);
  assert.deepEqual(push.cleanPushRows(["", "  "]), []);
});

test("the same addresses in another order are not a change", () => {
  assert.equal(push.samePushUrls([K1, F1], [F1, K1]), true);
  assert.equal(push.samePushUrls([K1], [K1, F1]), false);
  assert.equal(push.samePushUrls([K1, K2], [K1, F1]), false);
});

test("the collapsed summary counts addresses per channel", () => {
  assert.equal(push.pushSummary([]), "推送未配置");
  assert.equal(
    push.pushSummary(push.savedPushAddresses({ barkUrl: `${K1};${K2}`, feishuWebhook: F1 })),
    "推送：Bark 2 个、飞书 1 个",
  );
  assert.equal(push.pushSummary(push.savedPushAddresses({ barkUrl: "", feishuWebhook: F1 })), "推送：飞书 1 个");
});
