# Integration validation

Fixtures are synthetic; do not replace them with real account credentials.
The tests launch isolated hosts and never configure the running user service.

Download the CLIProxyAPI reference into the ignored root `CLIProxyAPI` directory
at commit `ac02da6c05e18f465aa7e3ed5b0a65a2f060917d`. Both Go test modules use a
local replace pointing to that reference. Build the plugin first.

```sh
(cd web-accounts && ./build.sh)
python3 integration/native-protocol-smoke.py web-accounts/web-accounts.dylib
(cd integration/execution-smoke && go test -v -count=10 -timeout=120s .)
(cd integration/host-smoke && go run . ../../web-accounts claude.synthetic.json kimi.synthetic.json)
```

`runtime-smoke.py` accepts an existing backend executable, plugin directory and
two synthetic auth fixtures. It starts its own temporary loopback service,
checks native registration/uploads/models and removes its temporary state.
It does not perform real-account inference. Linux uses `web-accounts.so` instead
of `.dylib`. Docker execution requires an actual container runtime.

## Chrome connector → native host

```sh
(cd chrome-extension && npm test)
node integration/extension-host-smoke.mjs /absolute/path/to/cli-proxy-api web-accounts
```

This test injects synthetic Chrome APIs/credentials into the actual extension
controller, but uses real HTTP against an isolated CLIProxyAPI executable and
our native plugin. It checks pairing, all three provider uploads, unique auth
records after the file watcher settles, namespaced models and session disconnect.
The test caught and now guards against mismatched upload/watcher auth IDs.
It is not a Chrome MV3 permission/runtime or live-provider inference test.

Optional upgrade regression (all paths local, synthetic isolated host only):

```sh
node integration/extension-host-smoke.mjs /absolute/path/to/cli-proxy-api web-accounts /absolute/path/to/previous/web-accounts.dylib
```

It loads the old library, stages current version 0.1.2 at a new path and changes a YAML
comment to trigger the file watcher. The check requires active `metadata.version`,
not only the discovered file path. A same-state config write alone may be skipped.

La prueba del conector también verifica el menú nativo **Cuentas web**, su
recurso HTML público sin credenciales, la política CSP y una ruta inexistente.
La versión esperada del binario actual es `0.1.2`.
