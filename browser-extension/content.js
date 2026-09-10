(() => {
  "use strict";

  const core = globalThis.APWAutoCheckoutCore;
  if (!core || !core.isSupportedAppleUrl(location.href)) return;

  const SESSION_KEY = "apwAutoCheckoutSessionV1";
  const CHECK_DELAY_MS = 700;
  const MAX_SESSION_MS = 30 * 60 * 1000;
  let running = false;
  let scheduled = 0;
  let lastClick = { signature: "", at: 0 };

  const storage = {
    get: () => new Promise((resolve) => chrome.storage.local.get(SESSION_KEY, (v) => resolve(v[SESSION_KEY] ?? null))),
    set: (value) => new Promise((resolve) => chrome.storage.local.set({ [SESSION_KEY]: value }, resolve)),
    clear: () => new Promise((resolve) => chrome.storage.local.remove(SESSION_KEY, resolve)),
  };

  function visible(element) {
    if (!(element instanceof HTMLElement) || element.hidden || element.getAttribute("aria-hidden") === "true") return false;
    const style = getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return style.display !== "none" && style.visibility !== "hidden" && rect.width > 0 && rect.height > 0;
  }

  function labelOf(element) {
    return core.normalizedText([
      element.innerText,
      element.textContent,
      element.getAttribute("aria-label"),
      element.getAttribute("title"),
      element instanceof HTMLInputElement ? element.value : "",
    ].filter(Boolean).join(" "));
  }

  function controls() {
    return [...document.querySelectorAll("button, a, [role='button'], label, input[type='button'], input[type='submit'], input[type='radio']")]
      .filter(visible);
  }

  function findControl(pattern, within = document) {
    return controls().find((element) => within.contains(element) && pattern.test(labelOf(element)));
  }

  function findChoice(pattern, within = document) {
    return controls().find((element) => element.tagName !== "A" && within.contains(element) && pattern.test(labelOf(element)));
  }

  function selected(element) {
    if (!element) return false;
    if (element.getAttribute("aria-checked") === "true" || element.getAttribute("aria-selected") === "true") return true;
    if (element instanceof HTMLInputElement) return element.checked;
    const forId = element.getAttribute("for");
    const input = (forId ? document.getElementById(forId) : null) ?? element.querySelector("input[type='radio'], input[type='checkbox']");
    return input instanceof HTMLInputElement && input.checked;
  }

  function safeClick(element, purpose) {
    if (!element || !visible(element)) return false;
    const label = labelOf(element);
    if (core.isForbiddenAction(label)) {
      void stop(`已阻止最终操作“${label || purpose}”，请你检查并付款`);
      return false;
    }
    const signature = `${location.pathname}|${purpose}|${label}`;
    const now = Date.now();
    if (signature === lastClick.signature && now - lastClick.at < 5000) return false;
    lastClick = { signature, at: now };
    show(`正在${purpose}${label ? `：${label}` : ""}`);
    element.click();
    return true;
  }

  function show(message, tone = "working") {
    let banner = document.getElementById("apw-auto-checkout-status");
    if (!banner) {
      banner = document.createElement("div");
      banner.id = "apw-auto-checkout-status";
      banner.setAttribute("role", "status");
      document.documentElement.appendChild(banner);
    }
    const text = `Apple Pickup Watcher：${message}`;
    if (banner.dataset.tone !== tone) banner.dataset.tone = tone;
    if (banner.textContent !== text) banner.textContent = text;
  }

  async function updateSession(session, patch) {
    const next = { ...session, ...patch };
    await storage.set(next);
    return next;
  }

  async function stop(message) {
    const session = await storage.get();
    if (session) await storage.set({ ...session, active: false, status: message });
    show(message, "stopped");
  }

  function hasPaymentField() {
    return Boolean(document.querySelector([
      "input[autocomplete='cc-number']",
      "input[name*='cardNumber' i]",
      "input[id*='card-number' i]",
      "iframe[title*='payment' i]",
      "iframe[name*='payment' i]",
    ].join(",")));
  }

  function pageContainsForbiddenAction() {
    return controls().some((element) => core.isForbiddenAction(labelOf(element)));
  }

  function selectNoExtras() {
    const noTradeIn = findControl(/(?:不参加折抵|无需折抵|不折抵|no\s+trade.?in)/i);
    if (noTradeIn && !selected(noTradeIn)) {
      return safeClick(noTradeIn, "选择不折抵");
    }
    const noAppleCare = findControl(/(?:不添加\s*AppleCare|无需\s*AppleCare|无\s*AppleCare|no\s+AppleCare)/i);
    if (noAppleCare && !selected(noAppleCare)) {
      return safeClick(noAppleCare, "选择不加 AppleCare");
    }
    return false;
  }

  async function selectPickup(session) {
    const storeNode = document.querySelector([
      `[data-store-number='${session.store}']`,
      `[data-store='${session.store}']`,
      `[data-retail-store='${session.store}']`,
      `[id*='${session.store}' i]`,
    ].join(","));
    if (storeNode) {
      const card = storeNode.closest("article, li, [role='listitem'], [class*='store' i]") ?? storeNode.parentElement;
      const choose = card && findChoice(/(?:选择此门店|选择门店|取货|select(?:\s+this)?\s+store|pick\s*up)/i, card);
      if (choose) {
        await updateSession(session, { storeSelected: true, status: `已锁定门店 ${session.store}` });
        return safeClick(choose, `选择目标门店 ${session.store}`) ? "acted" : "waiting";
      }
    }

    const pickup = findChoice(/(?:到店取货|店内取货|自行取货|I(?:'|’)ll\s+pick\s+it\s+up|pick\s*up)/i);
    if (pickup && !selected(pickup)) {
      return safeClick(pickup, "选择到店取货") ? "acted" : "waiting";
    }

    const searchInput = [...document.querySelectorAll("input")].find((input) => {
      if (!visible(input) || input.type === "password") return false;
      const hint = core.normalizedText([input.placeholder, input.getAttribute("aria-label"), input.name, input.id].filter(Boolean).join(" "));
      return /(?:城市|邮政编码|地点|门店|city|postal|zip|location|store)/i.test(hint);
    });
    if (searchInput && session.storeTitle && !session.searchedStore) {
      const query = session.storeTitle.split(/[-–—]/).filter(Boolean).at(-1) ?? session.storeTitle;
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
      setter?.call(searchInput, query);
      searchInput.dispatchEvent(new Event("input", { bubbles: true }));
      searchInput.dispatchEvent(new Event("change", { bubbles: true }));
      await updateSession(session, { searchedStore: true, status: `正在查找目标门店 ${session.storeTitle}` });
      const searchButton = findChoice(/^(?:搜索|查找|应用|search|find|apply)$/i);
      if (searchButton) safeClick(searchButton, `查找目标门店 ${session.storeTitle}`);
      else searchInput.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", code: "Enter", bubbles: true }));
      return "acted";
    }

    return pickup || searchInput ? "waiting" : "none";
  }

  async function step(session) {
    const bodyText = core.normalizedText(document.body?.innerText).slice(0, 50000);
    const inCheckout = /\/(?:checkout|order)(?:\/|$)/i.test(location.pathname);
    if (pageContainsForbiddenAction() || core.isPaymentPage({ url: location.href, text: inCheckout ? bodyText : "", hasPaymentField: hasPaymentField() })) {
      await stop("已到付款/最终确认阶段，自动操作已停止，请核对门店、型号和金额后手动付款");
      return;
    }

    if (document.querySelector("input[type='password'], input[autocomplete='current-password']")) {
      show("需要你手动登录 Apple 账户；登录完成后会自动继续", "waiting");
      return;
    }

    if (/\/shop\/bag(?:\/|$)/i.test(location.pathname)) {
      const checkout = findControl(/^(?:结账|去结账|checkout|check\s*out)$/i);
      if (!safeClick(checkout, "进入结账")) show("购物袋已打开，正在等待可用的结账按钮", "waiting");
      return;
    }

    if (/\/shop\/buy-iphone\//i.test(location.pathname)) {
      if (selectNoExtras()) return;
      if (!session.storeSelected) {
        const pickupResult = await selectPickup(session);
        if (pickupResult !== "none") {
          if (pickupResult === "waiting") show(`正在等待精确门店 ${session.store}；不会改选其他门店`, "waiting");
          return;
        }
      }
      if (session.addedToBag) {
        const viewBag = findControl(/^(?:查看购物袋|前往购物袋|view\s+(?:your\s+)?bag|go\s+to\s+bag)$/i);
        if (!safeClick(viewBag, "打开购物袋")) {
          show("已发送加购请求，正在等待 Apple 显示购物袋入口", "waiting");
        }
        return;
      }
      const add = findControl(/^(?:预购|添加到购物袋|加入购物袋|add\s+to\s+bag|pre-?order)$/i);
      if (add) {
        await updateSession(session, { addedToBag: true, status: "已发送加购请求" });
        safeClick(add, "加入购物袋");
      } else {
        show("正在等待 Apple 开放预购按钮", "waiting");
      }
      return;
    }

    if (/\/shop\/checkout/i.test(location.pathname) || /\/shop\/order/i.test(location.pathname)) {
      const pickupResult = await selectPickup(session);
      if (pickupResult === "acted") return;
      if (session.storeSelected) {
        const next = findControl(/^(?:继续|下一步|continue|next)$/i);
        if (safeClick(next, "继续到付款")) return;
      }
      show(`正在等待到店取货选项和目标门店 ${session.store}；找不到时不会改选其他门店`, "waiting");
      return;
    }

    show("Apple 页面结构暂未识别，自动操作已暂停且不会点击其他内容", "waiting");
  }

  async function run() {
    if (running) return;
    running = true;
    try {
      const fromUrl = core.sessionFromUrl(location.href);
      let session = fromUrl ?? await storage.get();
      if (fromUrl) {
        await storage.set(fromUrl);
        session = fromUrl;
      }
      if (!session) return;
      if (Date.now() > session.expiresAt || Date.now() - session.startedAt > MAX_SESSION_MS) {
        await storage.clear();
        show("任务已超过 30 分钟并自动失效", "stopped");
        return;
      }
      if (!session.active) {
        if (session.status) show(session.status, "stopped");
        return;
      }
      await step(session);
    } finally {
      running = false;
    }
  }

  function schedule() {
    clearTimeout(scheduled);
    scheduled = setTimeout(() => void run(), CHECK_DELAY_MS);
  }

  schedule();
  const observer = new MutationObserver(schedule);
  observer.observe(document.documentElement, { childList: true, subtree: true, attributes: true });
  addEventListener("pageshow", schedule);
})();
