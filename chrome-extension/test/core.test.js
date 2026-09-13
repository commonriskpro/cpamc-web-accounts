import test from "node:test";
import assert from "node:assert/strict";
import {
  CLAUDE_ORIGIN,
  KIMI_ORIGIN,
  MANAGEMENT_TTL_MS,
  createExtensionController,
  loadConnectionState,
  normalizeBackendURL,
  providerPayload,
  validatePluginStatus
} from "../core.js";

function response(status, data, {contentType = "application/json", redirected = false} = {}) {
  return {
    ok: status >= 200 && status < 300,
    status,
    redirected,
    headers: {get: (name) => name.toLowerCase() === "content-type" ? contentType : ""},
    json: async () => data
  };
}

function fakeChrome({local = {}, session = {}, cookie, tabs = [], token = "", permissions = true, permissionRequest} = {}) {
  const permissionCalls = [];
  const createdTabs = [];
  const cookieCalls = [];
  const area = (data) => ({
    async get(keys) {
      if (!Array.isArray(keys)) return {...data};
      return Object.fromEntries(keys.filter((key) => key in data).map((key) => [key, data[key]]));
    },
    async set(values) { Object.assign(data, values); },
    async remove(keys) { keys.forEach((key) => delete data[key]); }
  });
  return {
    chrome: {
      storage: {local: area(local), session: area(session)},
      permissions: {
        request: (value) => {
          permissionCalls.push(value);
          return typeof permissionRequest === "function" ? permissionRequest(value) : permissions;
        },
        contains: async () => true
      },
      cookies: {get: async (value) => { cookieCalls.push(value); return cookie || null; }},
      tabs: {
        query: async () => tabs,
        create: async (value) => { createdTabs.push(value); return value; }
      },
      scripting: {
        executeScript: async () => [{result: {token}}]
      },
      runtime: {id: "extension-id", getURL: (path) => `chrome-extension://extension-id/${path}`}
    },
    local,
    session,
    permissionCalls,
    cookieCalls,
    createdTabs
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return {promise, resolve, reject};
}

function pluginList() {
  return {plugins_enabled: true, plugins: [{id: "web-accounts", registered: true, enabled: true, effective_enabled: true}]};
}

test("backend URL policy rejects credentials, redirects, query and remote HTTP", () => {
  assert.equal(normalizeBackendURL("http://127.0.0.1:8317/management.html"), "http://127.0.0.1:8317");
  assert.equal(normalizeBackendURL("https://cpamc.example/"), "https://cpamc.example");
  for (const value of [
    "https://user:secret@cpamc.example/",
    "https://cpamc.example/?key=secret",
    "https://cpamc.example/#secret",
    "https://cpamc.example/other",
    "http://cpamc.example/"
  ]) assert.throws(() => normalizeBackendURL(value));
});

test("session key expires and is removed from session storage only", async () => {
  const fake = fakeChrome({local: {baseURL: "http://127.0.0.1:8317"}, session: {managementSession: {baseURL: "http://127.0.0.1:8317", managementKey: "secret", expiresAt: 50}}});
  const state = await loadConnectionState(fake.chrome, 51);
  assert.equal(state.managementKey, "");
  assert.deepEqual(fake.local, {baseURL: "http://127.0.0.1:8317"});
  assert.deepEqual(fake.session, {});
});

test("pair and Qoder upload use exact permission origin and never put key/PAT in URL", async () => {
  const fake = fakeChrome();
  const calls = [];
  const fetchImpl = async (url, options) => {
    calls.push({url, options});
    if (calls.length === 1) return response(200, pluginList());
    if (calls.length === 2) return response(200, {files: []});
    return response(200, {status: "ok"});
  };
  const controller = createExtensionController({chromeAPI: fake.chrome, fetchImpl, now: () => 1000, cryptoAPI: {randomUUID: () => "fixed-id"}});
  const paired = await controller.pairBackend("http://127.0.0.1:8317/management.html", "management-secret");
  assert.equal(paired.baseURL, "http://127.0.0.1:8317");
  assert.deepEqual(fake.local, {baseURL: "http://127.0.0.1:8317"});
  assert.deepEqual(fake.session.managementSession, {baseURL: "http://127.0.0.1:8317", managementKey: "management-secret", expiresAt: 1801000});
  const result = await controller.connectProvider("qoder", {personalAccessToken: "qoder-pat"});
  assert.equal(result.name, "web-accounts-qoder-fixed-id.json");
  assert.deepEqual(fake.permissionCalls[0], {origins: ["http://127.0.0.1:8317/*"]});
  assert.equal(fake.permissionCalls.length, 1);
  assert.equal(calls[0].options.headers.Authorization, "Bearer management-secret");
  assert.equal(calls[2].url.includes("qoder-pat"), false);
  assert.deepEqual(JSON.parse(calls[2].options.body), {type: "web-accounts", web_provider: "qoder", personal_access_token: "qoder-pat"});
});

test("provider upload rejects a visible backend URL changed after pairing", async () => {
  const fake = fakeChrome({session: {managementSession: {baseURL: "http://127.0.0.1:8317", managementKey: "m", expiresAt: Date.now() + MANAGEMENT_TTL_MS}}});
  const controller = createExtensionController({chromeAPI: fake.chrome, fetchImpl: async () => response(500, {})});
  await assert.rejects(() => controller.connectProvider("qoder", {expectedBaseURL: "https://other.example", personalAccessToken: "pat"}), {code: "backend-mismatch"});
  assert.equal(fake.permissionCalls.length, 0);
});

test("denied provider prompts are settled when pairing validation exits early", async () => {
  for (const scenario of ["not-paired", "backend-mismatch"]) {
    const prompt = deferred();
    const fake = fakeChrome({
      session: scenario === "backend-mismatch"
        ? {managementSession: {baseURL: "https://cpamc.example", managementKey: "old-key", expiresAt: 5000}}
        : {},
      permissionRequest: () => prompt.promise
    });
    const controller = createExtensionController({chromeAPI: fake.chrome, fetchImpl: async () => response(500, {}), now: () => 1000});
    const unhandled = [];
    const onUnhandled = (error) => unhandled.push(error);
    process.on("unhandledRejection", onUnhandled);
    try {
      const connect = controller.connectProvider("claude", scenario === "backend-mismatch" ? {expectedBaseURL: "https://other.example"} : {});
      await assert.rejects(connect, {code: scenario});
      prompt.reject(new Error("permission denied"));
      await new Promise((resolve) => setImmediate(resolve));
      assert.equal(unhandled.length, 0, `${scenario} left a denied permission rejection unhandled`);
    } finally {
      process.off("unhandledRejection", onUnhandled);
    }
  }
});

test("permission prompt rechecks pairing before capturing a provider session", async () => {
  for (const scenario of ["disconnect", "re-pair", "expire"]) {
    const prompt = deferred();
    let clock = 1000;
    const baseURL = "https://cpamc.example";
    const fake = fakeChrome({
      session: {managementSession: {baseURL, managementKey: "old-key", expiresAt: 2000}},
      permissionRequest: () => prompt.promise
    });
    const calls = [];
    const controller = createExtensionController({
      chromeAPI: fake.chrome,
      fetchImpl: async (url) => { calls.push(url); return response(500, {}); },
      now: () => clock
    });
    const connect = controller.connectProvider("claude", {expectedBaseURL: baseURL});
    await new Promise((resolve) => setImmediate(resolve));
    if (scenario === "disconnect") {
      await fake.chrome.storage.session.remove(["managementSession"]);
    } else if (scenario === "re-pair") {
      await fake.chrome.storage.session.set({managementSession: {baseURL, managementKey: "new-key", expiresAt: 2000}});
    } else {
      clock = 3000;
    }
    prompt.resolve(true);
    await assert.rejects(connect, {code: "session-changed"});
    assert.equal(fake.cookieCalls.length, 0, `${scenario} captured a provider cookie after pairing changed`);
    assert.equal(calls.length, 0, `${scenario} contacted the backend after pairing changed`);
  }
});

test("session mutation during capture prevents the upload POST", async () => {
  const capture = deferred();
  const baseURL = "https://cpamc.example";
  const fake = fakeChrome({
    session: {managementSession: {baseURL, managementKey: "old-key", expiresAt: 5000}}
  });
  fake.chrome.cookies.get = async (details) => {
    fake.cookieCalls.push(details);
    return capture.promise;
  };
  const calls = [];
  const controller = createExtensionController({
    chromeAPI: fake.chrome,
    fetchImpl: async (url) => { calls.push(url); return response(200, {status: "ok"}); },
    now: () => 1000,
    cryptoAPI: {randomUUID: () => "capture-id"}
  });
  const connect = controller.connectProvider("claude", {expectedBaseURL: baseURL});
  for (let i = 0; i < 10 && fake.cookieCalls.length === 0; i += 1) {
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.equal(fake.cookieCalls.length, 1, "capture did not start");
  await fake.chrome.storage.session.set({managementSession: {baseURL, managementKey: "new-key", expiresAt: 5000}});
  capture.resolve({value: "stale-session"});
  await assert.rejects(connect, {code: "session-changed"});
  assert.equal(calls.length, 0, "session mutation still allowed an upload request");
});

test("activation guard rechecks Management session before provider capture", async () => {
  const fake = fakeChrome({session: {managementSession: {baseURL: "https://cpamc.example", managementKey: "m", expiresAt: 5000}}, cookie: {value: "must-not-read"}});
  const calls = [];
  const controller = createExtensionController({
    chromeAPI: fake.chrome,
    fetchImpl: async (url) => { calls.push(url); return response(200, {files: []}); },
    now: () => 1000
  });
  const guard = async () => {
    await fake.chrome.storage.session.set({managementSession: {baseURL: "https://cpamc.example", managementKey: "new-key", expiresAt: 5000}});
  };
  await assert.rejects(
    () => controller.connectProvider("claude", {expectedBaseURL: "https://cpamc.example", activationGuard: guard}),
    {code: "session-changed"}
  );
  assert.equal(fake.cookieCalls.length, 0);
  assert.equal(calls.length, 0);
});

test("Claude capture is limited to sessionKey and Kimi uses exact .ai tab origin", async () => {
  const claude = fakeChrome({cookie: {value: "claude-session"}});
  const claudeCalls = [];
  const claudeController = createExtensionController({
    chromeAPI: claude.chrome,
    fetchImpl: async (url, options) => {
      claudeCalls.push({url, options});
      return response(200, claudeCalls.length === 1 ? {files: []} : {status: "ok"});
    },
    cryptoAPI: {randomUUID: () => "claude-id"}
  });
  await claude.chrome.storage.local.set({baseURL: "https://cpamc.example"});
  await claude.chrome.storage.session.set({managementSession: {baseURL: "https://cpamc.example", managementKey: "m", expiresAt: Date.now() + MANAGEMENT_TTL_MS}});
  await claudeController.connectProvider("claude");
  assert.deepEqual(JSON.parse(claudeCalls[1].options.body), {type: "web-accounts", web_provider: "claude", cookie: "sessionKey=claude-session"});
  assert.deepEqual(claude.permissionCalls[0], {permissions: ["cookies"], origins: [`${CLAUDE_ORIGIN}/*`]});

  const kimi = fakeChrome({tabs: [{id: 7, url: `${KIMI_ORIGIN}/chat/1`}], token: "kimi-token"});
  const kimiCalls = [];
  const kimiController = createExtensionController({
    chromeAPI: kimi.chrome,
    fetchImpl: async (url, options) => {
      kimiCalls.push({url, options});
      return response(200, kimiCalls.length === 1 ? {files: []} : {status: "ok"});
    },
    cryptoAPI: {randomUUID: () => "kimi-id"}
  });
  await kimi.chrome.storage.local.set({baseURL: "https://cpamc.example"});
  await kimi.chrome.storage.session.set({managementSession: {baseURL: "https://cpamc.example", managementKey: "m", expiresAt: Date.now() + MANAGEMENT_TTL_MS}});
  await kimiController.connectProvider("kimi");
  assert.deepEqual(JSON.parse(kimiCalls[1].options.body), {type: "web-accounts", web_provider: "kimi", access_token: "kimi-token"});
  assert.deepEqual(kimi.permissionCalls[0], {permissions: ["cookies", "scripting"], origins: [`${KIMI_ORIGIN}/*`]});
});

test("missing session opens the exact provider login and never uploads", async () => {
  const fake = fakeChrome();
  const controller = createExtensionController({chromeAPI: fake.chrome, fetchImpl: async () => response(500, {})});
  await fake.chrome.storage.local.set({baseURL: "http://127.0.0.1:8317"});
  await assert.rejects(() => controller.connectProvider("claude"), {code: "not-paired"});
  await fake.chrome.storage.session.set({managementSession: {baseURL: "http://127.0.0.1:8317", managementKey: "m", expiresAt: Date.now() + MANAGEMENT_TTL_MS}});
  await assert.rejects(() => controller.connectProvider("claude"), {code: "session-missing"});
  assert.equal(fake.createdTabs.at(-1).url, `${CLAUDE_ORIGIN}/`);
});

test("redirects, non-JSON responses, collisions and upload timeouts are safe errors", async () => {
  const fake = fakeChrome({local: {baseURL: "https://cpamc.example"}, session: {managementSession: {baseURL: "https://cpamc.example", managementKey: "management-secret", expiresAt: Date.now() + MANAGEMENT_TTL_MS}}});
  const redirectController = createExtensionController({chromeAPI: fake.chrome, fetchImpl: async () => response(200, pluginList(), {redirected: true})});
  await assert.rejects(() => redirectController.pairBackend("https://cpamc.example", "management-secret"), (error) => {
    assert.equal(error.code, "redirect");
    assert.equal(error.message.includes("management-secret"), false);
    return true;
  });
  const collisionController = createExtensionController({
    chromeAPI: fake.chrome,
    fetchImpl: async () => response(200, {files: [{name: "web-accounts-qoder-fixed-id.json"}]}),
    cryptoAPI: {randomUUID: () => "fixed-id"}
  });
  await assert.rejects(() => collisionController.connectProvider("qoder", {personalAccessToken: "pat"}), {code: "name-collision"});
  const timeoutController = createExtensionController({
    chromeAPI: fake.chrome,
    fetchImpl: async () => {
      if (!timeoutController.calls) timeoutController.calls = 0;
      timeoutController.calls += 1;
      if (timeoutController.calls === 1) return response(200, {files: []});
      const error = new Error("raw response secret");
      error.name = "AbortError";
      throw error;
    },
    cryptoAPI: {randomUUID: () => "timeout-id"}
  });
  await assert.rejects(() => timeoutController.connectProvider("qoder", {personalAccessToken: "pat"}), (error) => {
    assert.equal(error.code, "upload-uncertain");
    assert.equal(error.message.includes("raw response"), false);
    return true;
  });
  const errorResponseController = createExtensionController({
    chromeAPI: fake.chrome,
    fetchImpl: async () => response(200, {files: []})
  });
  await assert.rejects(() => errorResponseController.connectProvider("qoder", {personalAccessToken: "pat"}), {code: "upload-uncertain"});
});

test("plugin status requires registered and effective web-accounts", () => {
  assert.throws(() => validatePluginStatus({plugins_enabled: true, plugins: []}), {code: "plugin-missing"});
  assert.throws(() => validatePluginStatus({plugins_enabled: true, plugins: [{id: "web-accounts", registered: false, enabled: true}]}), {code: "plugin-unregistered"});
  assert.throws(() => validatePluginStatus({plugins_enabled: true, plugins: [{id: "web-accounts", registered: true, enabled: false}]}), {code: "plugin-disabled"});
});

test("credential payload rejects line breaks and keeps provider discriminator", () => {
  assert.deepEqual(providerPayload("kimi", "Bearer token"), {type: "web-accounts", web_provider: "kimi", access_token: "Bearer token"});
  assert.throws(() => providerPayload("claude", "sessionKey=x\nsecret"), {code: "credential-invalid"});
});
