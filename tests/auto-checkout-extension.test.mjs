import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import vm from "node:vm";

function loadCore() {
  const source = fs.readFileSync("browser-extension/core.js", "utf8");
  const sandbox = { URL };
  sandbox.globalThis = sandbox;
  vm.runInNewContext(source, sandbox, { filename: "browser-extension/core.js" });
  return sandbox.APWAutoCheckoutCore;
}

test("only an explicit Apple Store trigger creates an automation session", () => {
  const core = loadCore();
  const session = core.sessionFromUrl(
    "https://www.apple.com.cn/shop/buy-iphone/iphone-18-pro/mjyh4ch/a?apwAutoCheckout=1&apwStore=R532",
    1000,
  );
  assert.equal(session.part, "MJYH4CH/A");
  assert.equal(session.store, "R532");
  assert.equal(session.storeTitle, "");
  assert.equal(session.expiresAt, 1000 + 30 * 60 * 1000);
  assert.equal(core.sessionFromUrl("https://example.com/shop/x?apwAutoCheckout=1&apwStore=R532"), null);
  assert.equal(core.sessionFromUrl("https://www.apple.com.cn/shop/bag"), null);
  assert.equal(core.sessionFromUrl("https://www.apple.com.cn/shop/x/a/b?apwAutoCheckout=1&apwStore=R532%26bad"), null);
  assert.equal(core.isSupportedAppleUrl("https://secure4.store.apple.com/cn/shop/checkout/start"), true);
  assert.equal(core.isSupportedAppleUrl("https://www.apple.com/jp/shop/bag"), false);
  assert.equal(core.isSupportedAppleUrl("https://secure4.store.apple.com/jp/shop/checkout/start"), false);
});

test("final order and payment actions are always forbidden", () => {
  const core = loadCore();
  for (const label of ["立即下单", "提交订单", "支付订单", "Place your order", "Pay now"]) {
    assert.equal(core.isForbiddenAction(label), true, label);
  }
  assert.equal(core.isForbiddenAction("加入购物袋"), false);
  assert.equal(core.isForbiddenAction("继续"), false);
});

test("content automation records an add before clicking and never retries the add", () => {
  const source = fs.readFileSync("browser-extension/content.js", "utf8");
  const record = source.indexOf("addedToBag: true");
  const click = source.indexOf('safeClick(add, "加入购物袋")');
  assert.ok(record >= 0 && click > record, "the session must be recorded before the add click can navigate");
  assert.match(source, /if \(session\.addedToBag\)/);
});

test("payment and review pages stop the automation", () => {
  const core = loadCore();
  assert.equal(core.isPaymentPage({ text: "请选择付款方式" }), true);
  assert.equal(core.isPaymentPage({ url: "https://www.apple.com.cn/shop/checkout/payment" }), true);
  assert.equal(core.isPaymentPage({ hasPaymentField: true }), true);
  assert.equal(core.isPaymentPage({ text: "选择到店取货" }), false);
});

test("manifest grants access only to Apple Store pages", () => {
  const manifest = JSON.parse(fs.readFileSync("browser-extension/manifest.json", "utf8"));
  assert.deepEqual(manifest.permissions, ["storage"]);
  assert.ok(manifest.host_permissions.length > 0);
  assert.deepEqual(manifest.host_permissions, [
    "https://www.apple.com.cn/shop/*",
    "https://*.store.apple.com/cn/shop/*",
    "https://*.store.apple.com.cn/shop/*",
  ]);
  assert.deepEqual(manifest.content_scripts[0].matches, manifest.host_permissions);
  assert.equal(manifest.content_scripts[0].run_at, "document_idle");
  assert.equal(manifest.background.service_worker, "background.js");
});

test("checkout sessions are stored separately for each sender tab", () => {
  const source = fs.readFileSync("browser-extension/background.js", "utf8");
  assert.match(source, /sender\.tab\?\.id/);
  assert.match(source, /sessionKey\(tabId\)/);
  assert.doesNotMatch(source, /storage\.local/);
});
