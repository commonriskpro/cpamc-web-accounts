import test from "node:test";
import assert from "node:assert/strict";
import {createBackgroundController} from "../background.js";

function response(data, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    redirected: false,
    headers: {get: (name) => name.toLowerCase() === "content-type" ? "application/json" : ""},
    json: async () => data
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return {promise, resolve, reject};
}

function fakeChrome({url = "http://127.0.0.1:8317/management.html#/plugins"} = {}) {
  const local = {};
  const session = {};
  const permissionCalls = [];
  const scriptCalls = [];
  const tab = {id: 7, url};
  const marker = {value: "", documentId: "doc-1"};
  const area = (data) => ({
    async get(keys) { return Object.fromEntries(keys.filter((key) => key in data).map((key) => [key, data[key]])); },
    async set(values) { Object.assign(data, values); },
    async remove(keys) { for (const key of keys) delete data[key]; }
  });
  const runtime = {id: "extension-id", getURL: (path) => `chrome-extension://extension-id/${path}`};
  const chrome = {
    runtime,
    storage: {local: area(local), session: area(session)},
    action: {onClicked: {addListener() {}}, setBadgeText: async () => {}, setBadgeBackgroundColor: async () => {}, setTitle: async () => {}},
    permissions: {
      request: async (value) => { permissionCalls.push(value); return true; },
      contains: async () => true
    },
    tabs: {
      get: async () => ({...tab}),
      create: async () => {}
    },
    scripting: {
      executeScript: async (details) => {
        scriptCalls.push(details);
        const name = details.func?.name || "";
        if (name === "injectPanel") {
          marker.value = details.args[1];
          return [{documentId: marker.documentId, result: {documentNonce: marker.value}}];
        }
        if (name === "readDocumentMarker") return [{documentId: marker.documentId, result: marker.value}];
        if (name === "installManagementXHRHook") return [{documentId: marker.documentId, result: "management-secret"}];
        if (name === "cancelManagementXHRHook") return [{documentId: marker.documentId, result: true}];
        if (name === "removePanel") return [{documentId: marker.documentId, result: true}];
        return [{documentId: marker.documentId, result: true}];
      }
    }
  };
  return {chrome, local, session, tab, marker, permissionCalls, scriptCalls};
}

function sender(fake, token = "") {
  return {
    id: "extension-id",
    url: fake.chrome.runtime.getURL("panel.html"),
    tab: {id: fake.tab.id},
    token
  };
}

test("toolbar action accepts an SPA hash, requests exact origin, and mounts the panel", async () => {
  const fake = fakeChrome();
  let tokenCounter = 0;
  const controller = createBackgroundController({chromeAPI: fake.chrome, fetchImpl: async () => response({}), now: () => 1000, cryptoAPI: {randomUUID: () => `token-${++tokenCounter}`}});
  const activation = await controller.handleAction(fake.tab);
  assert.equal(activation.baseURL, "http://127.0.0.1:8317");
  assert.deepEqual(fake.permissionCalls[0], {permissions: ["scripting"], origins: ["http://127.0.0.1:8317/*"]});
  assert.equal(fake.session.cpamcWebAccountsActivations["7"].documentId, "doc-1");
  assert.equal(fake.scriptCalls.some((call) => call.func.name === "injectPanel" && call.world === "MAIN"), true);
});

test("background rejects forged sender and a navigated tab", async () => {
  const fake = fakeChrome();
  let tokenCounter = 0;
  const controller = createBackgroundController({chromeAPI: fake.chrome, fetchImpl: async () => response({}), now: () => 1000, cryptoAPI: {randomUUID: () => `token-${++tokenCounter}`}});
  const activation = await controller.handleAction(fake.tab);
  await assert.rejects(
    () => controller.handleMessage({type: "panel.hello"}, {id: "other-extension", url: fake.chrome.runtime.getURL("panel.html"), tab: {id: 7}}),
    {code: "sender-invalid"}
  );
  fake.tab.url = "https://other.example/management.html";
  await assert.rejects(() => controller.handleMessage({type: "panel.assert", token: activation.token}, sender(fake, activation.token)), {code: "activation-invalid"});
});

test("capture validates the candidate before storing an activation-bound session", async () => {
  const fake = fakeChrome();
  const calls = [];
  const controller = createBackgroundController({
    chromeAPI: fake.chrome,
    fetchImpl: async (url, options) => {
      calls.push({url, options});
      return response({plugins_enabled: true, plugins: [{id: "web-accounts", registered: true, enabled: true, effective_enabled: true}]});
    },
    now: () => 1000,
    cryptoAPI: {randomUUID: () => "activation-token"}
  });
  await controller.handleAction(fake.tab);
  const result = await controller.handleMessage({type: "panel.capture-session", token: "activation-token"}, sender(fake, "activation-token"));
  assert.equal(result.ok, true);
  assert.equal(result.activation.paired, true);
  assert.equal(fake.session.managementSession.managementKey, "management-secret");
  assert.equal(JSON.stringify(result).includes("management-secret"), false);
  assert.equal(calls.length, 1);
  fake.tab.url = "http://127.0.0.1:8317/management.html#/plugins";
  const disconnected = await controller.handleMessage({type: "panel.disconnect", token: "activation-token"}, sender(fake, "activation-token"));
  assert.equal(disconnected.disconnected, true);
  assert.equal(fake.session.managementSession, undefined);
  assert.equal(disconnected.activation.paired, false);
  const hello = await controller.handleMessage({type: "panel.hello"}, sender(fake));
  assert.equal(hello.activation.token, "activation-token");
  const closed = await controller.handleMessage({type: "panel.close", token: "activation-token"}, sender(fake, "activation-token"));
  assert.equal(closed.closed, true);
  assert.equal(fake.session.cpamcWebAccountsActivations["7"], undefined);
});

test("panel assert rejects an activation that has not paired or whose session expired", async () => {
  const fake = fakeChrome();
  const controller = createBackgroundController({chromeAPI: fake.chrome, fetchImpl: async () => response({}), now: () => 1000, cryptoAPI: {randomUUID: () => "activation-token"}});
  const activation = await controller.handleAction(fake.tab);
  await assert.rejects(() => controller.handleMessage({type: "panel.assert", token: activation.token}, sender(fake, activation.token)), {code: "pairing-required"});
  fake.session.cpamcWebAccountsActivations["7"].paired = true;
  fake.session.cpamcWebAccountsActivations["7"].pairedExpiresAt = 900;
  await assert.rejects(() => controller.handleMessage({type: "panel.assert", token: activation.token}, sender(fake, activation.token)), {code: "pairing-required"});
});

test("closing during a blocked pairing write cleans only its owned session", async () => {
  const fake = fakeChrome();
  const writeStarted = deferred();
  const oldWriteApplied = deferred();
  const allowOldWriteReturn = deferred();
  const originalSet = fake.chrome.storage.session.set;
  fake.chrome.storage.session.set = async (values) => {
    if (values?.managementSession?.ownerToken) {
      writeStarted.resolve();
      const result = await originalSet(values);
      oldWriteApplied.resolve();
      await allowOldWriteReturn.promise;
      return result;
    }
    return originalSet(values);
  };
  const controller = createBackgroundController({
    chromeAPI: fake.chrome,
    fetchImpl: async () => response({plugins_enabled: true, plugins: [{id: "web-accounts", registered: true, enabled: true, effective_enabled: true}]}),
    now: () => 1000,
    cryptoAPI: {randomUUID: () => "activation-token"}
  });
  const activation = await controller.handleAction(fake.tab);
  const capture = controller.handleMessage({type: "panel.capture-session", token: activation.token}, sender(fake, activation.token));
  await writeStarted.promise;
  const close = controller.handleMessage({type: "panel.close", token: activation.token}, sender(fake, activation.token));
  await new Promise((resolve) => setImmediate(resolve));
  await oldWriteApplied.promise;
  await originalSet({managementSession: {baseURL: "https://new.example", managementKey: "new-key", expiresAt: 9000, ownerToken: "new-owner"}});
  allowOldWriteReturn.resolve();
  await assert.rejects(capture, {code: "capture-cancelled"});
  await close;
  assert.equal(fake.session.managementSession.managementKey, "new-key");
});

test("re-opening the toolbar cancels a pending old capture without waiting for its timeout", async () => {
  const fake = fakeChrome();
  const hook = deferred();
  const originalExecute = fake.chrome.scripting.executeScript;
  fake.chrome.scripting.executeScript = async (details) => {
    if (details.func?.name === "installManagementXHRHook") return hook.promise;
    if (details.func?.name === "cancelManagementXHRHook") { hook.resolve([{documentId: "doc-1", result: ""}]); return [{result: true}]; }
    return originalExecute(details);
  };
  let tokenCounter = 0;
  const controller = createBackgroundController({chromeAPI: fake.chrome, fetchImpl: async () => response({}), now: () => 1000, cryptoAPI: {randomUUID: () => `token-${++tokenCounter}`}});
  const first = await controller.handleAction(fake.tab);
  const pending = controller.handleMessage({type: "panel.capture-session", token: first.token}, sender(fake, first.token));
  await new Promise((resolve) => setImmediate(resolve));
  const reopened = await controller.handleAction(fake.tab);
  assert.notEqual(reopened.token, first.token);
  await assert.rejects(pending);
});

test("old activation cannot pair a newly opened panel", async () => {
  const fake = fakeChrome();
  const controller = createBackgroundController({chromeAPI: fake.chrome, fetchImpl: async () => response({}), now: () => 1000, cryptoAPI: {randomUUID: () => "first-token"}});
  const first = await controller.handleAction(fake.tab);
  const secondController = createBackgroundController({chromeAPI: fake.chrome, fetchImpl: async () => response({}), now: () => 1000, cryptoAPI: {randomUUID: () => "second-token"}});
  await secondController.handleAction(fake.tab);
  await assert.rejects(() => controller.handleMessage({type: "panel.assert", token: first.token}, sender(fake, first.token)), {code: "activation-invalid"});
});
