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
