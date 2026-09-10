"use strict";

const SESSION_PREFIX = "apwAutoCheckoutTabSessionV1:";

function sessionKey(tabId) {
  return `${SESSION_PREFIX}${tabId}`;
}

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  const tabId = sender.tab?.id;
  if (!Number.isInteger(tabId) || !message || message.namespace !== "apw-auto-checkout") {
    sendResponse(null);
    return false;
  }

  const key = sessionKey(tabId);
  const reply = async () => {
    if (message.action === "get") {
      const saved = await chrome.storage.session.get(key);
      return saved[key] ?? null;
    }
    if (message.action === "set") {
      await chrome.storage.session.set({ [key]: message.value });
      return true;
    }
    if (message.action === "clear") {
      await chrome.storage.session.remove(key);
      return true;
    }
    return null;
  };

  void reply().then(sendResponse, () => sendResponse(null));
  return true;
});
