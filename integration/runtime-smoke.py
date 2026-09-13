"""Isolated CLIProxyAPI smoke check; pass synthetic auth fixtures, never real accounts."""
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


def main():
    binary, plugin_dir, *fixtures = map(lambda p: str(Path(p).resolve()), sys.argv[1:])
    assert len(fixtures) == 2, "Expected two synthetic auth fixtures"
    with tempfile.TemporaryDirectory(prefix="web-accounts-runtime-") as directory:
        root = Path(directory)
        (root / "auth").mkdir(mode=0o700)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        # Ephemeral test-only credentials, unrelated to any user account/service.
        management_key = "web-accounts-smoke-management-test-only"
        api_key = "web-accounts-smoke-api-test-only"
        config = root / "config.yaml"
        config.write_text(
            f"host: 127.0.0.1\nport: {port}\nauth-dir: {json.dumps(str(root / 'auth'))}\n"
            f"api-keys: [{api_key}]\nremote-management:\n  allow-remote: false\n"
            f"  secret-key: {management_key}\n  disable-control-panel: true\n"
            f"plugins:\n  enabled: true\n  dir: {json.dumps(plugin_dir)}\n"
            "  configs:\n    web-accounts:\n      enabled: true\n"
            "debug: false\nlogging-to-file: false\nusage-statistics-enabled: false\n"
        )
        config.chmod(0o600)
        url = f"http://127.0.0.1:{port}"

        def request(path, key=management_key, data=None, method=None):
            headers = {"Authorization": f"Bearer {key}"}
            if data is not None:
                headers["Content-Type"] = "application/json"
            req = urllib.request.Request(url + path, data=data, method=method, headers=headers)
            with urllib.request.urlopen(req, timeout=5) as response:
                return json.load(response)

        with (root / "server.log").open("w+") as log:
            child_env = {"PATH": os.environ.get("PATH", ""), "HOME": directory}
            if sys.platform == "win32":
                for var in (
                    "SystemRoot",
                    "WINDIR",
                    "ComSpec",
                    "SystemDrive",
                    "PATHEXT",
                    "TEMP",
                    "TMP",
                    "USERPROFILE",
                ):
                    val = os.environ.get(var)
                    if val is not None:
                        child_env[var] = val
            process = subprocess.Popen(
                [binary, "-local-model", "-config", str(config)], cwd=root,
                env=child_env,
                stdout=log, stderr=subprocess.STDOUT,
            )
            try:
                deadline = time.monotonic() + 30
                while True:
                    if process.poll() is not None:
                        raise RuntimeError("isolated server exited before readiness")
                    try:
                        plugins = request("/v0/management/plugins")
                        break
                    except (OSError, urllib.error.URLError):
                        if time.monotonic() >= deadline:
                            raise RuntimeError("isolated server readiness timeout")
                        time.sleep(0.1)
                entries = plugins.get("plugins", [])
                if not any(p.get("id") == "web-accounts" and p.get("registered") for p in entries):
                    print("Plugin listing:", json.dumps(plugins))
                    log.flush()
                    log.seek(0)
                    for line in log:
                        if "pluginhost:" in line:
                            print(line.rstrip())
                    raise AssertionError("plugin not registered")
                print("PASS: real Management API reports native plugin registered")
                for index, fixture in enumerate(fixtures):
                    raw = Path(fixture).read_bytes()
                    request(f"/v0/management/auth-files?name=web-smoke-{index}.json", data=raw, method="POST")
                deadline = time.monotonic() + 10
                while True:
                    accounts = request("/v0/management/auth-files")
                    models = request("/v1/models", key=api_key)
                    if len(accounts.get("files", [])) >= 2 and len(models.get("data", [])) > 0:
                        break
                    if time.monotonic() >= deadline:
                        raise RuntimeError("uploaded auth files did not produce models")
                    time.sleep(0.1)
                visible = json.dumps(accounts) + json.dumps(plugins) + json.dumps(models)
                for fixture in fixtures:
                    obj = json.loads(Path(fixture).read_text())
                    for field in ("cookie", "session_key", "sessionKey", "access_token", "token"):
                        value = obj.get(field)
                        if value:
                            assert value not in visible, "credential leaked in listing"
                ids = [m.get("id", "") for m in models["data"]]
                assert any("claude" in m for m in ids) and any("kimi" in m or m in ("k3", "k2d6") for m in ids), "provider models missing"
                assert not any("qwen" in m for m in ids), "unavailable Qwen models advertised"
                print("PASS: auth-file uploads register both providers and expose models; listings omit credentials")
                print("PASS: isolated native runtime integration; no inference or real accounts used")
            finally:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


if __name__ == "__main__":
    main()
