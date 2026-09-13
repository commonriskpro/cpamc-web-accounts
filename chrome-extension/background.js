import {
  backendPermissionOrigin,
  createExtensionController,
  extensionError,
  normalizeBackendURL
} from "./core.js";
import {
  cancelManagementXHRHook,
  injectPanel,
  installManagementXHRHook,
  readDocumentMarker,
  removePanel
} from "./page-hook.js";

export const ACTIVATIONS_KEY = "cpamcWebAccountsActivations";
export const ACTIVATION_TTL_MS = 30 * 60 * 1000;

function randomUUID(cryptoAPI = globalThis.crypto) {
  if (typeof cryptoAPI?.randomUUID === "function") return cryptoAPI.randomUUID();
  const bytes = new Uint8Array(16);
  if (typeof cryptoAPI?.getRandomValues === "function") cryptoAPI.getRandomValues(bytes);
  else for (let i = 0; i < bytes.length; i += 1) bytes[i] = Math.floor(Math.random() * 256);
  bytes[6] = (bytes[6] & 15) | 64;
  bytes[8] = (bytes[8] & 63) | 128;
  const hex = [...bytes].map((byte) => byte.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

function safeError(error) {
  if (error?.name === "ExtensionError") return {ok: false, code: error.code, message: error.message};
  return {ok: false, code: "operation-failed", message: "No se pudo completar la operación."};
}

function safeResult(result) {
  return {ok: true, ...result};
}

async function reportActionError(chromeAPI, tabId, error) {
  if (!Number.isInteger(tabId) || typeof chromeAPI?.action?.setBadgeText !== "function") return;
  try {
    if (!error) {
      await chromeAPI.action.setBadgeText({tabId, text: ""});
      if (typeof chromeAPI.action.setTitle === "function") {
        await chromeAPI.action.setTitle({tabId, title: "Conectar cuentas web"});
      }
      return;
    }
    const message = error?.name === "ExtensionError" ? error.message : "No se pudo abrir CPAMC Web Accounts.";
    await chromeAPI.action.setBadgeText({tabId, text: "!"});
    if (typeof chromeAPI.action.setBadgeBackgroundColor === "function") {
      await chromeAPI.action.setBadgeBackgroundColor({tabId, color: "#9b3e39"});
    }
    if (typeof chromeAPI.action.setTitle === "function") {
      await chromeAPI.action.setTitle({tabId, title: message});
    }
  } catch {
    // Badge APIs are unavailable in some test hosts and are not part of auth state.
  }
}

function asTargetTab(tab) {
  if (!Number.isInteger(tab?.id)) throw extensionError("tab-invalid", "La pestaña CPAMC no está disponible.");
  if (typeof tab.url !== "string") throw extensionError("tab-invalid", "Abre CPAMC en /management.html antes de usar la extensión.");
  let url;
  let baseURL;
  try {
    url = new URL(tab.url);
    if (url.username || url.password || url.search) throw new Error("unsafe-url");
    // CPAMC is an SPA: its hash may point at a Management subview, but the
    // backend origin remains the only network target we authorize.
    baseURL = normalizeBackendURL(`${url.origin}/`);
  } catch {
    throw extensionError("tab-invalid", "Abre CPAMC en /management.html antes de usar la extensión.");
  }
  if (url.pathname !== "/management.html") {
    throw extensionError("tab-invalid", "Abre CPAMC en /management.html antes de usar la extensión.");
  }
  return {tabId: tab.id, baseURL, origin: new URL(baseURL).origin};
}

function storageSession(chromeAPI) {
  if (!chromeAPI?.storage?.session) throw extensionError("storage-unavailable", "El almacenamiento de la extensión no está disponible.");
  return chromeAPI.storage.session;
}

async function readActivations(chromeAPI) {
  const result = await storageSession(chromeAPI).get([ACTIVATIONS_KEY]);
  const value = result?.[ACTIVATIONS_KEY];
  return value && typeof value === "object" ? {...value} : {};
}

async function writeActivations(chromeAPI, activations) {
  await storageSession(chromeAPI).set({[ACTIVATIONS_KEY]: activations});
}

async function clearOwnedSession(chromeAPI, ownerToken) {
  if (!ownerToken) return;
  const result = await storageSession(chromeAPI).get(["managementSession"]);
  if (result?.managementSession?.ownerToken !== ownerToken) return;
  await storageSession(chromeAPI).remove(["managementSession"]);
}

async function clearActivation(chromeAPI, tabId, expectedToken = "") {
  const activations = await readActivations(chromeAPI);
  const key = String(tabId);
  if (expectedToken && activations[key]?.token !== expectedToken) return;
  delete activations[key];
  await writeActivations(chromeAPI, activations);
}

function publicActivation(record) {
  return {
    tabId: record.tabId,
    baseURL: record.baseURL,
    token: record.token,
    expiresAt: record.expiresAt,
    paired: record.paired === true,
    pairedExpiresAt: Number(record.pairedExpiresAt || 0)
  };
}

export function createBackgroundController({
  chromeAPI = globalThis.chrome,
  fetchImpl = globalThis.fetch,
  now = Date.now,
  cryptoAPI = globalThis.crypto
} = {}) {
  if (!chromeAPI) throw extensionError("chrome-unavailable", "Chrome no está disponible.");
  const panelURL = chromeAPI.runtime.getURL("panel.html");
  const pendingCaptures = new Map();
  const generations = new Map();
  const locks = new Map();

  const nextGeneration = (tabId) => {
    const next = (generations.get(tabId) || 0) + 1;
    generations.set(tabId, next);
    return next;
  };

  const generationIsCurrent = (tabId, generation) => generations.get(tabId) === generation;

  async function withTabLock(tabId, task) {
    const previous = locks.get(tabId) || Promise.resolve();
    let release;
    const current = new Promise((resolve) => { release = resolve; });
    locks.set(tabId, current);
    await previous;
    try {
      return await task();
    } finally {
      release();
      if (locks.get(tabId) === current) locks.delete(tabId);
    }
  }

  async function getCurrentTab(tabId) {
    if (typeof chromeAPI?.tabs?.get !== "function") throw extensionError("tab-invalid", "La pestaña CPAMC no está disponible.");
    try {
      return await chromeAPI.tabs.get(tabId);
    } catch {
      throw extensionError("tab-invalid", "La pestaña CPAMC ya no está disponible.");
    }
  }

async function readPageMarker(tabId) {
    if (typeof chromeAPI?.scripting?.executeScript !== "function") {
      throw extensionError("permission-unavailable", "Chrome no permite validar la pestaña CPAMC.");
    }
    const result = await chromeAPI.scripting.executeScript({
      target: {tabId},
      world: "MAIN",
      func: readDocumentMarker
    });
    return {
      marker: result?.[0]?.result || "",
      documentId: result?.[0]?.documentId || ""
    };
  }

  async function validateActivation(sender, token = "", {allowHello = false, requirePaired = false} = {}) {
    if (sender?.id !== chromeAPI.runtime.id || sender.url !== panelURL) {
      throw extensionError("sender-invalid", "La solicitud de la extensión no es válida.");
    }
    const tabId = sender?.tab?.id;
    if (!Number.isInteger(tabId)) throw extensionError("sender-invalid", "La solicitud no pertenece a una pestaña CPAMC.");
    const activations = await readActivations(chromeAPI);
    const record = activations[String(tabId)];
    if (!record || (allowHello ? false : record.token !== token)) {
      throw extensionError("activation-invalid", "La pestaña CPAMC ya no está vinculada.");
    }
    if (!Number.isFinite(record.expiresAt) || record.expiresAt <= now()) {
      await clearActivation(chromeAPI, tabId, record.token);
      throw extensionError("activation-expired", "La vinculación de esta pestaña ha expirado.");
    }
    const tab = await getCurrentTab(tabId);
    let current;
    try {
      current = asTargetTab(tab);
    } catch {
      throw extensionError("activation-invalid", "La pestaña CPAMC cambió de origen o ruta.");
    }
    if (current.baseURL !== record.baseURL || current.tabId !== record.tabId) {
      throw extensionError("activation-invalid", "La pestaña CPAMC cambió de origen o ruta.");
    }
    const page = await readPageMarker(tabId);
    if (page.marker !== record.documentNonce || (record.documentId && page.documentId && record.documentId !== page.documentId)) {
      throw extensionError("activation-invalid", "La pestaña CPAMC se recargó; vuelve a abrir el panel.");
    }
    if (requirePaired) {
      if (record.paired !== true || !Number.isFinite(record.pairedExpiresAt) || record.pairedExpiresAt <= now()) {
        throw extensionError("pairing-required", "Vuelve a usar la sesión de CPAMC antes de conectar un proveedor.");
      }
      const session = await storageSession(chromeAPI).get(["managementSession"]);
      const managementSession = session?.managementSession;
      if (!managementSession?.managementKey || managementSession.baseURL !== record.baseURL ||
          Number(managementSession.expiresAt) !== Number(record.pairedExpiresAt) ||
          Number(managementSession.expiresAt) <= now()) {
        throw extensionError("pairing-required", "La sesión de CPAMC ya no está disponible; vuelve a usarla.");
      }
    }
    return record;
  }

  async function cancelPageHook(tabId) {
    try {
      await chromeAPI.scripting.executeScript({target: {tabId}, world: "MAIN", func: cancelManagementXHRHook});
    } catch {
      // Navigation or a closed tab already cancelled the page hook.
    }
  }

  async function invalidateActivation(tabId, token = "") {
    nextGeneration(tabId);
    await clearActivation(chromeAPI, tabId, token);
    await cancelPageHook(tabId);
  }

  async function requestActionPermissions(baseURL) {
    if (typeof chromeAPI?.permissions?.request !== "function") {
      throw extensionError("permission-unavailable", "Chrome no permite solicitar permisos para CPAMC.");
    }
    const granted = await chromeAPI.permissions.request({
      permissions: ["scripting"],
      origins: [backendPermissionOrigin(baseURL)]
    });
    if (!granted) throw extensionError("permission-denied", "Se necesita permiso para abrir el panel en este servidor.");
  }

  async function handleAction(tab) {
    const target = asTargetTab(tab);
    // This call must start directly from the toolbar gesture, before any await.
    const permissionPromise = requestActionPermissions(target.baseURL);
    await permissionPromise;
    const existingActivations = await readActivations(chromeAPI);
    const previous = existingActivations[String(target.tabId)];
    if (previous) {
      const pending = pendingCaptures.get(target.tabId);
      if (pending) pendingCaptures.delete(target.tabId);
      nextGeneration(target.tabId);
      await cancelPageHook(target.tabId);
      if (pending?.ownerToken) {
        void withTabLock(target.tabId, () => clearOwnedSession(chromeAPI, pending.ownerToken)).catch(() => {});
      }
    }
    const currentTab = await getCurrentTab(target.tabId);
    const currentTarget = asTargetTab(currentTab);
    if (currentTarget.baseURL !== target.baseURL) throw extensionError("tab-changed", "La pestaña CPAMC cambió antes de abrir el panel.");
    const token = randomUUID(cryptoAPI);
    const activation = {
      tabId: target.tabId,
      baseURL: target.baseURL,
      origin: target.origin,
      token,
      documentNonce: token,
      documentId: "",
      paired: false,
      pairedExpiresAt: 0,
      expiresAt: now() + ACTIVATION_TTL_MS
    };
    const activations = await readActivations(chromeAPI);
    activations[String(target.tabId)] = activation;
    await writeActivations(chromeAPI, activations);
    try {
      const result = await chromeAPI.scripting.executeScript({
        target: {tabId: target.tabId},
        world: "MAIN",
        func: injectPanel,
        args: [panelURL, token]
      });
      if (result?.[0]?.result?.documentNonce !== token) throw extensionError("panel-failed", "No se pudo abrir el panel CPAMC.");
      activation.documentId = result?.[0]?.documentId || "";
      const updated = await readActivations(chromeAPI);
      if (updated[String(target.tabId)]?.token === token) {
        updated[String(target.tabId)] = activation;
        await writeActivations(chromeAPI, updated);
      }
      return publicActivation(activation);
    } catch (error) {
      await clearActivation(chromeAPI, target.tabId, token);
      if (error?.name === "ExtensionError") throw error;
      throw extensionError("panel-failed", "No se pudo abrir el panel CPAMC.");
    }
  }

  async function captureSession(sender, message) {
    const activation = await validateActivation(sender, message?.token);
    const tabId = activation.tabId;
    if (pendingCaptures.has(tabId)) throw extensionError("capture-busy", "Ya hay una captura de sesión en curso.");
    const generation = nextGeneration(tabId);
    const ownerToken = randomUUID(cryptoAPI);
    const capture = (async () => {
      const result = await chromeAPI.scripting.executeScript({
        target: {tabId},
        world: "MAIN",
        func: installManagementXHRHook,
        args: [activation.origin]
      });
      const key = result?.[0]?.result;
      if (!generationIsCurrent(tabId, generation) || typeof key !== "string" || key.trim() === "") {
        throw extensionError("capture-failed", "No se pudo capturar la sesión de CPAMC.");
      }
      const controller = createExtensionController({chromeAPI, fetchImpl, now, cryptoAPI});
      const paired = await withTabLock(tabId, async () => {
        if (!generationIsCurrent(tabId, generation)) throw extensionError("capture-cancelled", "La captura de sesión fue cancelada.");
        const current = await validateActivation(sender, message?.token);
        const paired = await controller.pairExistingPermission(current.baseURL, key, async () => {
          if (!generationIsCurrent(tabId, generation)) throw extensionError("capture-cancelled", "La captura de sesión fue cancelada.");
          await validateActivation(sender, message?.token);
        }, ownerToken);
        if (!generationIsCurrent(tabId, generation)) throw extensionError("capture-cancelled", "La captura de sesión fue cancelada.");
        const activations = await readActivations(chromeAPI);
        const currentActivation = activations[String(tabId)];
        if (!currentActivation || currentActivation.token !== message?.token) {
          throw extensionError("capture-cancelled", "La captura de sesión fue cancelada.");
        }
        activations[String(tabId)] = {...currentActivation, paired: true, pairedExpiresAt: paired.expiresAt};
        await writeActivations(chromeAPI, activations);
        return {baseURL: paired.baseURL, expiresAt: paired.expiresAt, activation: publicActivation(activations[String(tabId)])};
      });
      return paired;
    })();
    const pending = {promise: capture, ownerToken, generation};
    pendingCaptures.set(tabId, pending);
    try {
      return await capture;
    } finally {
      if (pendingCaptures.get(tabId) === pending) pendingCaptures.delete(tabId);
    }
  }

  async function handleMessage(message, sender) {
    if (!message || typeof message.type !== "string") throw extensionError("message-invalid", "La solicitud de la extensión no es válida.");
    if (message.type === "panel.hello") {
      const activation = await validateActivation(sender, "", {allowHello: true});
      return safeResult({activation: publicActivation(activation)});
    }
    const activation = await validateActivation(sender, message.token, {requirePaired: message.type === "panel.assert"});
    if (message.type === "panel.assert") return safeResult({activation: publicActivation(activation)});
    if (message.type === "panel.capture-session") return safeResult(await captureSession(sender, message));
    if (message.type === "panel.disconnect") {
      // Keep the tab/document activation so this panel can reconnect or close;
      // only the pairing and any pending capture are invalidated.
      nextGeneration(activation.tabId);
      await cancelPageHook(activation.tabId);
      await withTabLock(activation.tabId, async () => {
        const controller = createExtensionController({chromeAPI, fetchImpl, now, cryptoAPI});
        await controller.disconnect();
        const activations = await readActivations(chromeAPI);
        const current = activations[String(activation.tabId)];
        if (current?.token === activation.token) {
          activations[String(activation.tabId)] = {...current, paired: false, pairedExpiresAt: 0};
          await writeActivations(chromeAPI, activations);
        }
      });
      const activations = await readActivations(chromeAPI);
      return safeResult({disconnected: true, activation: publicActivation(activations[String(activation.tabId)] || activation)});
    }
    if (message.type === "panel.close") {
      const pending = pendingCaptures.get(activation.tabId);
      await invalidateActivation(activation.tabId, activation.token);
      await withTabLock(activation.tabId, () => clearOwnedSession(chromeAPI, pending?.ownerToken || ""));
      await chromeAPI.scripting.executeScript({target: {tabId: activation.tabId}, world: "MAIN", func: removePanel});
      return safeResult({closed: true});
    }
    throw extensionError("message-invalid", "La solicitud de la extensión no es válida.");
  }

  return {handleAction, handleMessage, validateActivation};
}

export function registerBackground(chromeAPI = globalThis.chrome) {
  const controller = createBackgroundController({chromeAPI});
  chromeAPI.action.onClicked.addListener((tab) => {
    void controller.handleAction(tab)
      .then(() => reportActionError(chromeAPI, tab?.id, null))
      .catch((error) => reportActionError(chromeAPI, tab?.id, error));
  });
  chromeAPI.runtime.onMessage.addListener((message, sender, sendResponse) => {
    void controller.handleMessage(message, sender)
      .then(sendResponse)
      .catch((error) => sendResponse(safeError(error)));
    return true;
  });
  return controller;
}

if (globalThis.chrome?.action?.onClicked && globalThis.chrome?.runtime?.onMessage) registerBackground(globalThis.chrome);
