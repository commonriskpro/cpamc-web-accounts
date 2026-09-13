export function injectPanel(panelURL, documentNonce) {
  const panelHostID = "cpamc-web-accounts-panel";
  const documentMarker = "__cpamc_web_accounts_document_nonce__";
  const existing = document.getElementById(panelHostID);
  if (existing) existing.remove();
  window[documentMarker] = String(documentNonce || "");

  const host = document.createElement("div");
  host.id = panelHostID;
  // Keep the host inspectable for accessibility/UI automation; the iframe's
  // extension origin remains the security boundary for panel controls.
  const shadow = host.attachShadow({mode: "open"});
  const frame = document.createElement("iframe");
  frame.title = "CPAMC Web Accounts";
  frame.src = panelURL;
  frame.setAttribute("allow", "clipboard-read; clipboard-write");
  frame.style.cssText = "border:0;width:100%;height:100%;display:block;background:#f7fbfa";
  shadow.append(frame);
  host.style.cssText = "position:fixed;inset:0 0 0 auto;width:min(420px,100vw);z-index:2147483647;background:#f7fbfa;box-shadow:-12px 0 40px rgba(10,31,45,.18)";
  (document.documentElement || document.body).append(host);
  return {href: location.href, documentNonce: window[documentMarker]};
}

export function removePanel() {
  const host = document.getElementById("cpamc-web-accounts-panel");
  if (host) host.remove();
  return true;
}

export function readDocumentMarker() {
  return window["__cpamc_web_accounts_document_nonce__"] || "";
}

export function installManagementXHRHook(expectedOrigin) {
  const hookStateKey = "__cpamc_web_accounts_xhr_hook__";
  const origin = String(expectedOrigin || "");
  if (location.origin !== origin) return Promise.reject(new Error("origin-mismatch"));
  const existing = window[hookStateKey];
  if (existing?.promise) return existing.promise;

  const prototype = XMLHttpRequest.prototype;
  const originalOpen = prototype.open;
  const originalSetRequestHeader = prototype.setRequestHeader;
  const originalSend = prototype.send;
  let resolveResult;
  let rejectResult;
  let settled = false;
  const requests = new WeakMap();
  const requestRecords = new Set();
  const promise = new Promise((resolve, reject) => {
    resolveResult = resolve;
    rejectResult = reject;
  });
  const state = {promise, cancel: () => finish(new Error("cancelled"))};
  window[hookStateKey] = state;

  const cleanup = () => {
    clearTimeout(timeout);
    // Do not overwrite wrappers installed after this hook.
    if (prototype.open === wrappedOpen) prototype.open = originalOpen;
    if (prototype.setRequestHeader === wrappedSetRequestHeader) prototype.setRequestHeader = originalSetRequestHeader;
    if (prototype.send === wrappedSend) prototype.send = originalSend;
    if (window[hookStateKey] === state) delete window[hookStateKey];
  };
  const finish = (error, value) => {
    if (settled) return;
    settled = true;
    for (const request of requestRecords) request.bearer = "";
    requestRecords.clear();
    cleanup();
    if (error) rejectResult(error);
    else resolveResult(value);
  };
  const timeout = setTimeout(() => finish(new Error("capture-timeout")), 60000);

  function wrappedOpen(method, url) {
    if (settled) return originalOpen.apply(this, arguments);
    const request = {method: String(method || "").toUpperCase(), url: String(url || ""), bearer: ""};
    try {
      const parsed = new URL(request.url, location.href);
      if (request.method !== "GET" || parsed.origin !== origin || !parsed.pathname.startsWith("/v0/management/")) {
        requests.set(this, null);
        return originalOpen.apply(this, arguments);
      }
    } catch {
      requests.set(this, null);
      return originalOpen.apply(this, arguments);
    }
    requests.set(this, request);
    requestRecords.add(request);
    return originalOpen.apply(this, arguments);
  }

  function wrappedSetRequestHeader(name, value) {
    if (settled) return originalSetRequestHeader.apply(this, arguments);
    const request = requests.get(this);
    if (request && String(name || "").toLowerCase() === "authorization" && typeof value === "string") {
      const match = /^Bearer\s+(\S.*)$/i.exec(value);
      if (match && !/[\r\n]/.test(match[1])) request.bearer = match[1].trim();
    }
    return originalSetRequestHeader.apply(this, arguments);
  }

  function wrappedSend(body) {
    if (settled) return originalSend.apply(this, arguments);
    const request = requests.get(this);
    const candidate = request?.bearer?.trim() || "";
    let result;
    try {
      result = originalSend.apply(this, arguments);
    } catch (error) {
      if (request) request.bearer = "";
      finish(error);
      throw error;
    }
    if (candidate) {
      request.bearer = "";
      finish(null, candidate);
    }
    return result;
  }

  prototype.open = wrappedOpen;
  prototype.setRequestHeader = wrappedSetRequestHeader;
  prototype.send = wrappedSend;
  return promise;
}

export function cancelManagementXHRHook() {
  const state = window["__cpamc_web_accounts_xhr_hook__"];
  if (state?.cancel) state.cancel();
  return true;
}
