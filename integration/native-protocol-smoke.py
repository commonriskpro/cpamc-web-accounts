"""Exercise the actual c-shared binary against ABI-shaped synthetic host callbacks.

No network, accounts, browser, source edits, or host settings are used.
Wire shapes pinned to CLIProxyAPI ac02da6c sdk/pluginapi and internal/pluginhost.
"""
import base64
import ctypes as C
import json
from pathlib import Path
import struct
import sys
import threading


class Buffer(C.Structure):
    _fields_ = [("ptr", C.c_void_p), ("length", C.c_size_t)]


HostCall = C.CFUNCTYPE(C.c_int, C.c_void_p, C.c_char_p, C.c_void_p, C.c_size_t, C.POINTER(Buffer))
Free = C.CFUNCTYPE(None, C.c_void_p, C.c_size_t)
PluginCall = C.CFUNCTYPE(C.c_int, C.c_char_p, C.c_void_p, C.c_size_t, C.POINTER(Buffer))
Shutdown = C.CFUNCTYPE(None)


class Host(C.Structure):
    _fields_ = [("version", C.c_uint32), ("ctx", C.c_void_p), ("call", HostCall), ("free", Free)]


class Plugin(C.Structure):
    _fields_ = [("version", C.c_uint32), ("call", PluginCall), ("free", Free), ("shutdown", Shutdown)]


def b64(value):
    if not isinstance(value, bytes):
        value = json.dumps(value).encode()
    return base64.b64encode(value).decode()


def frame(value, flags=0):
    raw = json.dumps(value).encode()
    return bytes([flags]) + struct.pack(">I", len(raw)) + raw


def main():
    library = C.CDLL(str(Path(sys.argv[1]).resolve()))
    buffers = {}
    failures = []
    emitted = []
    finished = threading.Event()
    active_provider = "claude"
    pending = []
    claude_stream = b"".join(b"data: " + json.dumps(e).encode() + b"\n\n" for e in [
        {"type": "message_start", "message": {"id": "fixture", "role": "assistant"}},
        {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}},
        {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "hello"}},
        {"type": "content_block_stop", "index": 0},
        {"type": "message_delta", "delta": {"stop_reason": "end_turn"}},
        {"type": "message_stop"},
    ])
    kimi_stream = frame({"op": "set", "mask": "block.text", "block": {"text": {"content": "hello"}}}) + frame({}, 2)

    @HostCall
    def call_host(ctx, method, request, length, response):
        nonlocal pending
        try:
            method = method.decode()
            req = json.loads(C.string_at(request, length))
            result = {}
            if method in ("host.http.do", "host.http.do_stream"):
                assert req.get("host_callback_id") == "fixture-callback", "request-scoped callback ID missing"
                req = req.get("request", req)
                req = {k.lower(): v for k, v in req.items()}
                if req["method"] == "GET":
                    assert req["url"] == "https://claude.ai/api/organizations"
                    result = {"StatusCode": 200, "Headers": {}, "Body": b64([{"uuid": "00000000-0000-4000-8000-000000000001"}])}
                else:
                    raw = base64.b64decode(req["body"])
                    body = json.loads(raw if active_provider == "claude" else raw[5:])
                    assert "fixture-system" in json.dumps(body), "system instruction dropped"
                    stream = claude_stream if active_provider == "claude" else kimi_stream
                    if method == "host.http.do":
                        result = {"StatusCode": 200, "Headers": {}, "Body": b64(stream)}
                    else:
                        pending = [stream[:7], stream[7:]]
                        result = {"status_code": 200, "headers": {}, "stream_id": "fixture-upstream"}
            elif method == "host.http.stream_read":
                assert req["stream_id"] == "fixture-upstream"
                result = {"payload": b64(pending.pop(0)), "done": False} if pending else {"done": True}
            elif method == "host.http.stream_close":
                assert req["stream_id"] == "fixture-upstream"
            elif method == "host.stream.emit":
                assert req["stream_id"] == "fixture-output"
                emitted.append(base64.b64decode(req["payload"]))
            elif method == "host.stream.close":
                assert not req.get("error"), "plugin stream reported an error"
                finished.set()
            else:
                raise AssertionError("unexpected host method: " + method)
            envelope = {"ok": True, "result": result}
        except Exception as exc:
            failures.append(str(exc))
            finished.set()
            envelope = {"ok": False, "error": {"code": "fixture_failure", "message": "synthetic host rejected callback"}}
        data = json.dumps(envelope).encode()
        buf = C.create_string_buffer(data)
        pointer = C.addressof(buf)
        buffers[pointer] = buf
        response.contents.ptr = pointer
        response.contents.length = len(data)
        return 0

    @Free
    def free_host(pointer, length):
        buffers.pop(pointer, None)

    host = Host(1, None, call_host, free_host)
    plugin = Plugin()
    library.cliproxy_plugin_init.argtypes = [C.POINTER(Host), C.POINTER(Plugin)]
    assert library.cliproxy_plugin_init(C.byref(host), C.byref(plugin)) == 0

    def invoke(method, value, expected_ok=True):
        raw = json.dumps(value).encode()
        buf = C.create_string_buffer(raw)
        response = Buffer()
        plugin.call(method.encode(), buf, len(raw), C.byref(response))
        try:
            result = json.loads(C.string_at(response.ptr, response.length))
        finally:
            plugin.free(response.ptr, response.length)
        assert result["ok"] == expected_ok, f"unexpected native status for {method}: {result}"
        return result.get("result")

    invoke("plugin.register", {"schema_version": 6})
    duplicate = Plugin()
    assert library.cliproxy_plugin_init(C.byref(host), C.byref(duplicate)) != 0, "active instance was overwritten"
    invoke("plugin.quiesce", {})
    invoke("executor.identifier", {}, expected_ok=False)
    invoke("plugin.reconfigure", {"schema_version": 6})
    invoke("executor.identifier", {})
    print("PASS: duplicate init rejected and quiesce/reconfigure restores service")
    for provider, model in [("claude", "web-claude/claude-sonnet-4-6"), ("kimi", "web-kimi/k3")]:
        active_provider = provider
        for streaming in (False, True):
            emitted.clear()
            finished.clear()
            credential = {"web_provider": provider, "cookie": "sessionKey=synthetic"} if provider == "claude" else {"web_provider": provider, "access_token": "synthetic"}
            body = {"model": model, "messages": [{"role": "system", "content": "fixture-system"}, {"role": "user", "content": "hello"}], "stream": streaming}
            request = {"AuthID": "fixture-auth", "AuthProvider": "web-accounts", "Model": model, "Format": "chat-completions", "Stream": streaming, "StorageJSON": b64(credential), "Payload": b64(body), "host_callback_id": "fixture-callback"}
            if streaming:
                request["stream_id"] = "fixture-output"
            result = invoke("executor.execute_stream" if streaming else "executor.execute", request)
            if streaming:
                assert finished.wait(5), "native stream never closed"
                assert not failures, "; ".join(failures)
                assert emitted[-1] == b"data: [DONE]\n\n", "missing successful DONE"
                chunks = [json.loads(c[6:]) for c in emitted[:-1]]
                assert all(c["object"] == "chat.completion.chunk" for c in chunks)
                assert len({c["id"] for c in chunks}) == 1, "completion ID changes within one response"
                assert "".join(c["choices"][0]["delta"].get("content", "") for c in chunks) == "hello"
            else:
                completion = json.loads(base64.b64decode(result["Payload"]))
                assert completion["choices"][0]["message"]["content"] == "hello"
            print(f"PASS: native {provider} {'stream' if streaming else 'JSON'} protocol, synthetic host only")
    plugin.shutdown()
    invoke("plugin.reconfigure", {}, expected_ok=False)
    assert library.cliproxy_plugin_init(C.byref(host), C.byref(plugin)) == 0
    invoke("plugin.register", {"schema_version": 6})
    invoke("executor.identifier", {})
    plugin.shutdown()
    print("PASS: shutdown prevents reconfigure; reinit after complete shutdown works")


if __name__ == "__main__":
    main()
