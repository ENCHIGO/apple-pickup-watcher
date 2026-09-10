(() => {
  "use strict";

  const FINAL_ACTION = /(?:立即下单|提交订单|确认购买|确认下单|现在支付|立即支付|支付订单|place\s+(?:your\s+)?order|buy\s+now|pay\s+now|confirm\s+(?:purchase|order))/i;
  const PAYMENT_TEXT = /(?:付款方式|支付方式|输入银行卡|信用卡或借记卡|支付宝|微信支付|payment\s+method|billing\s+information|credit\s+card)/i;

  function normalizedText(value) {
    return String(value ?? "").replace(/\s+/g, " ").trim();
  }

  function isSupportedAppleUrl(value) {
    try {
      const url = new URL(value);
      const mainlandPublicStore = url.hostname === "www.apple.com.cn"
        && url.pathname.startsWith("/shop/");
      const mainlandSecureStore = (url.hostname.endsWith(".store.apple.com")
        && url.pathname.startsWith("/cn/shop/"))
        || (url.hostname.endsWith(".store.apple.com.cn") && url.pathname.startsWith("/shop/"));
      return url.protocol === "https:" && (mainlandPublicStore || mainlandSecureStore);
    } catch {
      return false;
    }
  }

  function isForbiddenAction(value) {
    return FINAL_ACTION.test(normalizedText(value));
  }

  function isPaymentPage({ url = "", text = "", hasPaymentField = false } = {}) {
    const pathLooksFinal = /\/(?:payment|review|place-order)(?:\/|$)/i.test(String(url));
    return Boolean(hasPaymentField || pathLooksFinal || PAYMENT_TEXT.test(normalizedText(text)));
  }

  function sessionFromUrl(value, now = Date.now()) {
    if (!isSupportedAppleUrl(value)) return null;
    const url = new URL(value);
    if (url.searchParams.get("apwAutoCheckout") !== "1") return null;
    const store = normalizedText(url.searchParams.get("apwStore"));
    if (!/^[A-Za-z0-9-]+$/.test(store)) return null;
    const pathParts = url.pathname.split("/").filter(Boolean);
    const part = pathParts.slice(-2).join("/").toUpperCase();
    if (!/^[A-Z0-9-]+\/[A-Z0-9-]+$/.test(part)) return null;
    return {
      active: true,
      part,
      store,
      storeTitle: normalizedText(url.searchParams.get("apwStoreTitle")),
      startedAt: now,
      expiresAt: now + 30 * 60 * 1000,
      storeSelected: false,
      searchedStore: false,
      status: "已接收自动结账任务",
    };
  }

  globalThis.APWAutoCheckoutCore = Object.freeze({
    isForbiddenAction,
    isPaymentPage,
    isSupportedAppleUrl,
    normalizedText,
    sessionFromUrl,
  });
})();
