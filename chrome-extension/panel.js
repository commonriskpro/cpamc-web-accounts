import {createExtensionController, extensionError, displayBackendHost} from "./core.js";

const controller = createExtensionController({chromeAPI: chrome, fetchImpl: fetch});
const serverHost = document.querySelector("#server-host");
const serverDot = document.querySelector("#server-dot");
const status = document.querySelector("#status");
const useSessionButton = document.querySelector("#use-session");
const disconnectButton = document.querySelector("#disconnect");
const qoderPAT = document.querySelector("#qoder-pat");
let activation = null;
let paired = false;
let closed = false;

function messageFor(error) {
  if (error?.name === "ExtensionError") return error.message;
  return "No se pudo completar la operación.";
}

function setStatus(text, kind = "") {
  status.textContent = text;
  status.className = `notice ${kind}`.trim();
}

function setBusy(button, busy) {
  button.disabled = busy;
  button.setAttribute("aria-busy", busy ? "true" : "false");
}

function setProvidersEnabled(enabled) {
  document.querySelectorAll(".provider-button").forEach((button) => { button.disabled = !enabled; });
  qoderPAT.disabled = !enabled;
}

async function send(message) {
  const result = await chrome.runtime.sendMessage(message);
  if (!result?.ok) {
    throw extensionError(result?.code || "operation-failed", result?.message || "No se pudo completar la operación.");
  }
  return result;
}

async function refreshActivation() {
  const result = await send({type: "panel.hello"});
  activation = result.activation;
  serverHost.textContent = displayBackendHost(activation.baseURL);
  serverDot.classList.add("ready");
  const state = await controller.state();
  paired = activation.paired === true &&
    Boolean(state.managementKey && state.baseURL === activation.baseURL && state.expiresAt === activation.pairedExpiresAt);
  setProvidersEnabled(paired);
  setStatus(paired ? "Sesión de CPAMC disponible." : "Pulsa Usar sesión de CPAMC y luego Refresh All.", paired ? "success" : "");
}

async function useSession() {
  if (!activation || closed) return;
  setBusy(useSessionButton, true);
  setStatus("Observando la próxima petición de Management…");
  try {
    const result = await send({type: "panel.capture-session", token: activation.token});
    activation = result.activation;
    paired = true;
    setProvidersEnabled(true);
    setStatus(`Sesión de CPAMC verificada para ${displayBackendHost(result.baseURL)}.`, "success");
  } catch (error) {
    setStatus(messageFor(error), "error");
  } finally {
    setBusy(useSessionButton, false);
  }
}

async function connectProvider(button) {
  if (!activation || !paired || closed) return;
  const provider = button.dataset.provider;
  const token = activation.token;
  setBusy(button, true);
  setStatus(`Solicitando permiso y preparando ${provider}…`);
  try {
    const options = provider === "qoder"
      ? {personalAccessToken: qoderPAT.value, expectedBaseURL: activation.baseURL}
      : {expectedBaseURL: activation.baseURL};
    const guard = async () => {
      const result = await send({type: "panel.assert", token});
      activation = result.activation;
      return result;
    };
    const result = await controller.connectProvider(provider, {...options, activationGuard: guard});
    setStatus(`${result.message} Archivo: ${result.name}`, "success");
  } catch (error) {
    setStatus(messageFor(error), "error");
  } finally {
    qoderPAT.value = "";
    setBusy(button, false);
  }
}

async function disconnect() {
  if (!activation || closed) return;
  setBusy(disconnectButton, true);
  try {
    const result = await send({type: "panel.disconnect", token: activation.token});
    activation = result.activation;
    paired = false;
    setProvidersEnabled(false);
    setStatus("Sesión desconectada. Puedes volver a usar la sesión de CPAMC.", "success");
  } catch (error) {
    setStatus(messageFor(error), "error");
  } finally {
    setBusy(disconnectButton, false);
  }
}

async function closePanel() {
  if (closed) return;
  closed = true;
  setProvidersEnabled(false);
  if (activation) {
    try { await send({type: "panel.close", token: activation.token}); } catch { /* the tab may have navigated */ }
  }
}

useSessionButton.addEventListener("click", useSession);
disconnectButton.addEventListener("click", disconnect);
document.querySelector("#close").addEventListener("click", () => { void closePanel(); });
document.querySelectorAll(".provider-button").forEach((button) => {
  button.addEventListener("click", () => { void connectProvider(button); });
});
window.addEventListener("pagehide", () => {
  if (!closed && activation) void send({type: "panel.close", token: activation.token}).catch(() => {});
});

void refreshActivation().catch((error) => {
  setProvidersEnabled(false);
  serverDot.classList.remove("ready");
  setStatus(messageFor(error), "error");
});
