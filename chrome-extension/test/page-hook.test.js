import test from "node:test";
import assert from "node:assert/strict";
import vm from "node:vm";
import {cancelManagementXHRHook, installManagementXHRHook} from "../page-hook.js";

function makePage({origin = "http://127.0.0.1:8317", sendThrows = false} = {}) {
  const original = {
    open(method, url) { this.method = method; this.url = url; },
    setRequestHeader(name, value) { this.headers[name] = value; },
    send(body) {
      this.body = body;
      if (sendThrows) throw new Error("send failed");
    }
  };
  class FakeXHR {
    constructor() { this.headers = {}; }
  }
  FakeXHR.prototype.open = original.open;
  FakeXHR.prototype.setRequestHeader = original.setRequestHeader;
  FakeXHR.prototype.send = original.send;
  const context = {
    window: {XMLHttpRequest: FakeXHR},
    XMLHttpRequest: FakeXHR,
    URL,
    Promise,
    Error,
    location: {origin, href: `${origin}/management.html`},
    setTimeout,
    clearTimeout
  };
  const install = vm.runInNewContext(`(${installManagementXHRHook.toString()})`, context);
  const cancel = vm.runInNewContext(`(${cancelManagementXHRHook.toString()})`, context);
  return {context, FakeXHR, original, install, cancel};
}

test("XHR hook captures only eligible GET bearer requests and restores prototypes", async () => {
  const page = makePage();
  const open = page.FakeXHR.prototype.open;
  const setRequestHeader = page.FakeXHR.prototype.setRequestHeader;
  const send = page.FakeXHR.prototype.send;
  const promise = page.install("http://127.0.0.1:8317");
  const xhr = new page.FakeXHR();
  xhr.open("GET", "/v0/management/plugins");
  xhr.setRequestHeader("Authorization", "Bearer management-secret");
  xhr.send("body that must not be inspected");
  assert.equal(await promise, "management-secret");
  assert.equal(xhr.body, "body that must not be inspected");
  assert.equal(page.FakeXHR.prototype.open, open);
  assert.equal(page.FakeXHR.prototype.setRequestHeader, setRequestHeader);
  assert.equal(page.FakeXHR.prototype.send, send);
});

test("XHR hook ignores other origins and paths, rejects wrong entry origin, and cancels safely", async () => {
  const page = makePage();
  const promise = page.install("http://127.0.0.1:8317");
  const external = new page.FakeXHR();
  external.open("GET", "https://other.example/v0/management/plugins");
  external.setRequestHeader("Authorization", "Bearer external-secret");
  external.send("");
  const otherPath = new page.FakeXHR();
  otherPath.open("GET", "/v1/other");
  otherPath.setRequestHeader("Authorization", "Bearer ignored-secret");
  otherPath.send("");
  page.cancel();
  await assert.rejects(promise, /cancelled/);

  const wrongOrigin = makePage({origin: "https://cpamc.example"});
  await assert.rejects(() => wrongOrigin.install("https://other.example"), /origin-mismatch/);
  assert.equal(wrongOrigin.FakeXHR.prototype.open, wrongOrigin.original.open);
});

test("XHR hook captures only after send succeeds and does not retain secrets after completion", async () => {
  const page = makePage({sendThrows: true});
  const promise = page.install("http://127.0.0.1:8317");
  const xhr = new page.FakeXHR();
  xhr.open("GET", "/v0/management/plugins");
  xhr.setRequestHeader("Authorization", "Bearer not-sent");
  assert.throws(() => xhr.send(""), /send failed/);
  await assert.rejects(promise, /send failed/);

  const wrapped = makePage();
  const capture = wrapped.install("http://127.0.0.1:8317");
  const hookedSend = wrapped.FakeXHR.prototype.send;
  wrapped.FakeXHR.prototype.send = function (...args) { return hookedSend.apply(this, args); };
  const first = new wrapped.FakeXHR();
  first.open("GET", "/v0/management/plugins");
  first.setRequestHeader("Authorization", "Bearer first-secret");
  first.send("");
  assert.equal(await capture, "first-secret");
  const later = new wrapped.FakeXHR();
  later.open("GET", "/v0/management/plugins");
  later.setRequestHeader("Authorization", "Bearer later-secret");
  later.send("");
  assert.equal(later.headers.Authorization, "Bearer later-secret");
});
