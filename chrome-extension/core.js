export const DEFAULT_BACKEND_URL = "http://127.0.0.1:8317";
export const MANAGEMENT_TTL_MS = 30 * 60 * 1000;
export const MAX_RESPONSE_BYTES = 1 << 20;
export const CLAUDE_ORIGIN = "https://claude.ai";
export const KIMI_ORIGIN = "https://www.kimi.ai";
export const PROVIDERS = ["claude", "kimi", "qoder"];

class ExtensionError extends Error {
  constructor(code, message) {
    super(message);
    this.name = "ExtensionError";
    this.code = code;
  }
}

export function extensionError(code, message) {
  return new ExtensionError(code, message);
}

export function normalizeBackendURL(input) {
  if (typeof input !== "string" || input.trim() === "") {
    throw extensionError("invalid-url", "Escribe la URL del servidor CPAMC.");
  }
  let url;
  try {
    url = new URL(input.trim());
  } catch {
    throw extensionError("invalid-url", "La URL del servidor no es válida.");
  }
  if (url.username || url.password || url.search || url.hash) {
    throw extensionError("invalid-url", "La URL no puede incluir credenciales, consulta ni fragmento.");
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") {
    throw extensionError("invalid-url", "El servidor debe usar HTTPS o HTTP local.");
  }
  const hostname = url.hostname.toLowerCase();
  const localHTTP = url.protocol === "http:" &&
    (hostname === "localhost" || hostname === "127.0.0.1" || hostname === "[::1]");
  if (url.protocol === "http:" && !localHTTP) {
    throw extensionError("invalid-url", "HTTP solo está permitido para localhost, 127.0.0.1 o [::1].");
  }
  if (url.pathname !== "/" && url.pathname !== "/management.html") {
    throw extensionError("invalid-url", "La ruta debe ser / o /management.html.");
  }
  return url.origin;
}

export function backendPermissionOrigin(baseURL) {
  return `${new URL(normalizeBackendURL(baseURL)).origin}/*`;
}

export function displayBackendHost(baseURL) {
  return new URL(normalizeBackendURL(baseURL)).host;
}

function storageArea(chromeAPI, area) {
  if (!chromeAPI?.storage?.[area]) {
    throw extensionError("storage-unavailable", "El almacenamiento de la extensión no está disponible.");
  }
  return chromeAPI.storage[area];
}

export async function loadConnectionState(chromeAPI, now = Date.now()) {
  const local = await storageArea(chromeAPI, "local").get(["baseURL"]);
  const session = await storageArea(chromeAPI, "session").get(["managementSession"]);
  const paired = session.managementSession;
  const expiresAt = Number(paired?.expiresAt || 0);
  if (!paired?.managementKey || !paired?.baseURL || !Number.isFinite(expiresAt) || expiresAt <= now) {
    if (paired) {
      await storageArea(chromeAPI, "session").remove(["managementSession", "managementKey", "managementKeyExpiresAt"]);
    }
    return {baseURL: normalizeBackendURL(local.baseURL || DEFAULT_BACKEND_URL), managementKey: "", expiresAt: 0};
  }
  return {baseURL: normalizeBackendURL(paired.baseURL), managementKey: paired.managementKey, expiresAt};
}

export async function disconnect(chromeAPI) {
  await storageArea(chromeAPI, "session").remove(["managementSession", "managementKey", "managementKeyExpiresAt"]);
  return loadConnectionState(chromeAPI);
}

async function requestOriginPermission(chromeAPI, baseURL) {
  if (typeof chromeAPI?.permissions?.request !== "function") {
    throw extensionError("permission-unavailable", "Chrome no permite solicitar el acceso al servidor.");
  }
  const granted = await chromeAPI.permissions.request({origins: [backendPermissionOrigin(baseURL)]});
  if (!granted) throw extensionError("permission-denied", "Se necesita permiso para contactar este servidor.");
}

async function requestProviderPermissions(chromeAPI, provider) {
  const origins = provider === "claude" ? [`${CLAUDE_ORIGIN}/*`] : [`${KIMI_ORIGIN}/*`];
  const permissions = provider === "claude" ? ["cookies"] : ["cookies", "scripting"];
  if (typeof chromeAPI?.permissions?.request !== "function") {
    throw extensionError("permission-unavailable", "Chrome no permite solicitar permisos de cuentas web.");
  }
  const granted = await chromeAPI.permissions.request({permissions, origins});
  if (!granted) throw extensionError("permission-denied", "Se necesita permiso para leer la cuenta elegida.");
}

function responseIsJSON(response) {
  const type = response?.headers?.get?.("content-type") || "";
  return type.toLowerCase().split(";", 1)[0].trim() === "application/json";
}

function statusError(status, operation) {
  if (status === 401 || status === 403) return extensionError("unauthorized", "La clave de Management fue rechazada.");
  if (status === 404) return extensionError("not-found", `La ruta de ${operation} no está disponible en este servidor.`);
  if (status >= 500) return extensionError("server-error", `El servidor no pudo completar ${operation}.`);
  return extensionError("http-error", `${operation} rechazó la solicitud (${status}).`);
}

async function responseJSON(response, operation) {
  const length = Number(response?.headers?.get?.("content-length") || 0);
  if (Number.isFinite(length) && length > MAX_RESPONSE_BYTES) {
    throw extensionError("response-too-large", `La respuesta de ${operation} es demasiado grande.`);
  }
  if (response?.body?.getReader) {
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let total = 0;
    let text = "";
    try {
      while (true) {
        const chunk = await reader.read();
        if (chunk.done) break;
        total += chunk.value?.byteLength || 0;
        if (total > MAX_RESPONSE_BYTES) {
          await reader.cancel();
          throw extensionError("response-too-large", `La respuesta de ${operation} es demasiado grande.`);
        }
        text += decoder.decode(chunk.value, {stream: true});
      }
      text += decoder.decode();
      return JSON.parse(text);
    } catch (error) {
      if (error?.name === "ExtensionError") throw error;
      throw extensionError("invalid-response", `La respuesta de ${operation} no es válida.`);
    }
  }
  if (typeof response?.text === "function") {
    const text = await response.text();
    if (new TextEncoder().encode(text).byteLength > MAX_RESPONSE_BYTES) {
      throw extensionError("response-too-large", `La respuesta de ${operation} es demasiado grande.`);
    }
    try {
      return JSON.parse(text);
    } catch {
      throw extensionError("invalid-response", `La respuesta de ${operation} no es válida.`);
    }
  }
  const value = await response.json();
  try {
    if (new TextEncoder().encode(JSON.stringify(value)).byteLength > MAX_RESPONSE_BYTES) {
      throw extensionError("response-too-large", `La respuesta de ${operation} es demasiado grande.`);
    }
  } catch (error) {
    if (error?.name === "ExtensionError") throw error;
    throw extensionError("invalid-response", `La respuesta de ${operation} no es válida.`);
  }
  return value;
}

async function fetchJSON(fetchImpl, url, options, operation, timeoutMs = 8000, uncertain = false) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await fetchImpl(url, {...options, redirect: "error", signal: controller.signal});
    if (response?.redirected) throw extensionError("redirect", "El servidor intentó redirigir la solicitud.");
    if (!response?.ok) {
      if (uncertain) throw extensionError("upload-uncertain", "No se pudo confirmar la subida; verifica Auth Files antes de reintentar.");
      throw statusError(response.status, operation);
    }
    if (!responseIsJSON(response)) throw extensionError("content-type", `La respuesta de ${operation} no es JSON.`);
    return await responseJSON(response, operation);
  } catch (error) {
    if (error?.name === "ExtensionError") throw error;
    if (error?.name === "AbortError") throw extensionError("timeout", `${operation} tardó demasiado.`);
    if (uncertain) throw extensionError("upload-uncertain", "No se pudo confirmar la subida; verifica Auth Files antes de reintentar.");
    throw extensionError("network", `No se pudo contactar el servidor para ${operation}.`);
  } finally {
    clearTimeout(timer);
  }
}

function authorizationHeaders(managementKey) {
  return {Authorization: `Bearer ${managementKey}`, Accept: "application/json"};
}

function pluginEntry(payload) {
  const plugins = Array.isArray(payload?.plugins) ? payload.plugins : [];
  return plugins.find((item) => item && item.id === "web-accounts") || null;
}

export function validatePluginStatus(payload) {
  const plugin = pluginEntry(payload);
  if (!plugin) throw extensionError("plugin-missing", "No se encontró el plugin web-accounts.");
  if (!plugin.registered) throw extensionError("plugin-unregistered", "web-accounts está instalado pero no registrado.");
  const effective = Object.prototype.hasOwnProperty.call(plugin, "effective_enabled")
    ? plugin.effective_enabled === true
    : (payload.plugins_enabled === true && plugin.enabled === true && plugin.registered === true);
  if (!effective) throw extensionError("plugin-disabled", "web-accounts está registrado pero desactivado.");
  return plugin;
}

async function checkPlugin(fetchImpl, baseURL, managementKey) {
  const payload = await fetchJSON(fetchImpl, `${baseURL}/v0/management/plugins`, {
    method: "GET", headers: authorizationHeaders(managementKey)
  }, "la comprobación del plugin");
  return validatePluginStatus(payload);
}

export function providerPayload(provider, credential) {
  if (!PROVIDERS.includes(provider) || typeof credential !== "string" || credential.trim() === "") {
    throw extensionError("credential-missing", "No se encontró una credencial para esa cuenta.");
  }
  if (/[\r\n]/.test(credential)) throw extensionError("credential-invalid", "La credencial contiene saltos de línea inválidos.");
  const value = credential.trim();
  const payload = {type: "web-accounts", web_provider: provider};
  if (provider === "claude") payload.cookie = value;
  else if (provider === "kimi") payload.access_token = value;
  else payload.personal_access_token = value;
  return payload;
}

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

async function assertUniqueAuthName(fetchImpl, baseURL, managementKey, name) {
  const payload = await fetchJSON(fetchImpl, `${baseURL}/v0/management/auth-files`, {
    method: "GET", headers: authorizationHeaders(managementKey)
  }, "la comprobación de archivos");
  const files = Array.isArray(payload?.files) ? payload.files : [];
  if (files.some((file) => file && (file.name === name || file.id === name || file.file_name === name))) {
    throw extensionError("name-collision", "El nombre generado ya existe; verifica Auth Files antes de reintentar.");
  }
}

async function uploadAuth(fetchImpl, baseURL, managementKey, provider, payload, cryptoAPI, beforePost) {
  const name = `web-accounts-${provider}-${randomUUID(cryptoAPI)}.json`;
  await assertUniqueAuthName(fetchImpl, baseURL, managementKey, name);
  const body = JSON.stringify(payload);
  if (beforePost) await beforePost();
  let result;
  try {
    result = await fetchJSON(fetchImpl, `${baseURL}/v0/management/auth-files?name=${encodeURIComponent(name)}`, {
      method: "POST",
      headers: {...authorizationHeaders(managementKey), "Content-Type": "application/json"},
      body
    }, "la subida", 8000, true);
  } catch (error) {
    if (error?.code === "upload-uncertain") throw error;
    throw extensionError("upload-uncertain", "No se pudo confirmar la subida; verifica Auth Files antes de reintentar.");
  }
  if (result?.status !== "ok") {
    throw extensionError("upload-uncertain", "No se pudo confirmar la subida; verifica Auth Files antes de reintentar.");
  }
  return name;
}

async function openLogin(chromeAPI, origin) {
  if (typeof chromeAPI?.tabs?.create === "function") {
    await chromeAPI.tabs.create({url: `${origin}/`});
  }
}

async function captureClaude(chromeAPI) {
  const cookie = await chromeAPI.cookies.get({url: `${CLAUDE_ORIGIN}/`, name: "sessionKey"});
  if (!cookie?.value) {
    await openLogin(chromeAPI, CLAUDE_ORIGIN);
    throw extensionError("session-missing", "Inicia sesión en Claude y vuelve a Conectar.");
  }
  return `sessionKey=${cookie.value}`;
}

async function captureKimi(chromeAPI) {
  let tabs = [];
  try {
    tabs = await chromeAPI.tabs.query({url: [`${KIMI_ORIGIN}/*`]});
  } catch {
    tabs = [];
  }
  const tab = (Array.isArray(tabs) ? tabs : []).find((item) => {
    try { return Number.isInteger(item?.id) && new URL(item.url || KIMI_ORIGIN).origin === KIMI_ORIGIN; } catch { return false; }
  });
  if (tab && typeof chromeAPI?.scripting?.executeScript === "function") {
    try {
      const result = await chromeAPI.scripting.executeScript({
        target: {tabId: tab.id},
        world: "ISOLATED",
        func: (expectedOrigin) => {
          if (location.origin !== expectedOrigin) return {token: ""};
          return {token: localStorage.getItem("access_token") || ""};
        },
        args: [KIMI_ORIGIN]
      });
      const token = result?.[0]?.result?.token;
      if (typeof token === "string" && token.trim() !== "") return token;
    } catch {
      // Cookie fallback below is intentionally limited to kimi-auth.
    }
  }
  const cookie = await chromeAPI.cookies.get({url: `${KIMI_ORIGIN}/`, name: "kimi-auth"});
  if (cookie?.value) return `kimi-auth=${cookie.value}`;
  await openLogin(chromeAPI, KIMI_ORIGIN);
  throw extensionError("session-missing", "Inicia sesión en Kimi y vuelve a Conectar.");
}

export function createExtensionController({chromeAPI = globalThis.chrome, fetchImpl = globalThis.fetch, now = Date.now, cryptoAPI = globalThis.crypto} = {}) {
  if (typeof fetchImpl !== "function") throw extensionError("fetch-unavailable", "Fetch no está disponible en esta extensión.");
  return {
    async state() {
      return loadConnectionState(chromeAPI, now());
    },
    async pairBackend(baseURLInput, managementKey) {
      const baseURL = normalizeBackendURL(baseURLInput);
      if (typeof managementKey !== "string" || managementKey.trim() === "") {
        throw extensionError("key-missing", "Escribe la clave de Management.");
      }
      await requestOriginPermission(chromeAPI, baseURL);
      const plugin = await checkPlugin(fetchImpl, baseURL, managementKey);
      const expiresAt = now() + MANAGEMENT_TTL_MS;
      await storageArea(chromeAPI, "local").set({baseURL});
      await storageArea(chromeAPI, "session").set({managementSession: {baseURL, managementKey, expiresAt}});
      return {baseURL, plugin, expiresAt};
    },
    async pairExistingPermission(baseURLInput, managementKey, beforeSave, ownerToken = "") {
      const baseURL = normalizeBackendURL(baseURLInput);
      if (typeof managementKey !== "string" || managementKey.trim() === "") {
        throw extensionError("key-missing", "No se encontró una clave de Management.");
      }
      if (typeof chromeAPI?.permissions?.contains !== "function") {
        throw extensionError("permission-unavailable", "No se pudo comprobar el permiso del servidor.");
      }
      const granted = await chromeAPI.permissions.contains({origins: [backendPermissionOrigin(baseURL)]});
      if (!granted) throw extensionError("permission-denied", "Se necesita permiso para contactar este servidor.");
      const plugin = await checkPlugin(fetchImpl, baseURL, managementKey);
      const expiresAt = now() + MANAGEMENT_TTL_MS;
      if (typeof beforeSave === "function") await beforeSave();
      const managementSession = {baseURL, managementKey, expiresAt};
      if (ownerToken) managementSession.ownerToken = ownerToken;
      await storageArea(chromeAPI, "session").set({managementSession});
      await storageArea(chromeAPI, "local").set({baseURL});
      return {baseURL, plugin, expiresAt};
    },
    async disconnect() {
      return disconnect(chromeAPI);
    },
    async connectProvider(provider, options = {}) {
      if (!PROVIDERS.includes(provider)) throw extensionError("provider-invalid", "Proveedor no disponible.");
      const normalizedExpected = options.expectedBaseURL ? normalizeBackendURL(options.expectedBaseURL) : "";
      // Start this synchronously from the click handler, but settle rejection immediately so
      // validation failures below cannot leave a denied permission prompt unhandled.
      const permissionPromise = provider === "qoder" ? null : requestProviderPermissions(chromeAPI, provider)
        .then(() => ({ok: true}), (error) => ({ok: false, error}));
      const state = await loadConnectionState(chromeAPI, now());
      if (!state.managementKey) throw extensionError("not-paired", "Empareja primero un servidor CPAMC.");
      if (normalizedExpected && normalizedExpected !== state.baseURL) {
        throw extensionError("backend-mismatch", "La URL visible cambió; vuelve a emparejar el servidor antes de conectar.");
      }
      const baseURL = state.baseURL;
      if (permissionPromise) {
        const permission = await permissionPromise;
        if (!permission.ok) throw permission.error;
      }
      const assertCurrentSession = async () => {
        const current = await loadConnectionState(chromeAPI, now());
        if (current.baseURL !== state.baseURL ||
            current.managementKey !== state.managementKey ||
            current.expiresAt !== state.expiresAt) {
          throw extensionError("session-changed", "La sesión de Management cambió; vuelve a iniciar la conexión.");
        }
      };
      await assertCurrentSession();
      if (typeof options.activationGuard === "function") {
        await options.activationGuard();
        await assertCurrentSession();
      }
      let credential;
      if (provider === "claude") credential = await captureClaude(chromeAPI);
      else if (provider === "kimi") credential = await captureKimi(chromeAPI);
      else credential = options.personalAccessToken || "";
      const payload = providerPayload(provider, credential);
      await assertCurrentSession();
      if (typeof options.activationGuard === "function") {
        await options.activationGuard();
        await assertCurrentSession();
      }
      const name = await uploadAuth(
        fetchImpl,
        baseURL,
        state.managementKey,
        provider,
        payload,
        cryptoAPI,
        async () => {
          await assertCurrentSession();
          if (typeof options.activationGuard === "function") {
            await options.activationGuard();
            await assertCurrentSession();
          }
        }
      );
      return {name, provider, message: "Cuenta añadida; inferencia no comprobada."};
    }
  };
}
