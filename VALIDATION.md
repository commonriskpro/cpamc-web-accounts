# Validation — 2026-09-12

## Result

Native `web-accounts` v0.1.0 is installed and enabled in the local macOS Apple
Silicon CLIProxyAPI v7.2.158 backend. CPAMC's Plugin Management UI confirmed
**Effective · Registered · Configured** with Enabled selected. Its two existing
plugins remained effective. The backend process did not restart.

Only `plugins.configs.web-accounts.enabled: true` was added to the backend
configuration; the original file was backed up with restricted permissions.
No CPAMC/backend source, existing provider settings or account credentials were
changed. The existing API remained reachable with its same 25 model entries.

## Checks

- Go unit tests, race detector, vet and native build: PASS.
- Compiled C ABI: duplicate-init protection, quiesce/reconfigure, shutdown/reinit,
  Claude/Kimi JSON and streaming: PASS.
- Actual Go host: three test groups repeated ten times, including HTTP error
  handling and active-stream unload: PASS (30 load/unload cycles).
- Installed backend executable in an isolated temporary service: native plugin
  registration, synthetic auth-file uploads, provider model isolation and
  credential omission from listings: PASS.
- Independent source/contract review: PASS.
- Live CPAMC UI and loaded process library: installed/registered/enabled confirmed.
- macOS Intel: explicitly unsupported; build script rejects it.
- Linux amd64 and arm64, both glibc and musl: cross compilation PASS. Linux and
  Docker runtime execution was NOT performed on this macOS host.

Installed macOS ARM64 binary SHA256:
`ea9362d1e96fb62a3c065124b513dbd226936fb19b3fd26958e4c4c1b39a7c25`

## Not yet validated / not included

No real Claude/Kimi cookie or Qoder PAT has been used. Synthetic fixtures prove
integration/protocol handling, not live-account inference or provider access.
Qwen Web (`chat.qwen.ai`) is not included. Qoder/Qwen3.8 is a separate, buffered
CLI connector requiring the official Qoder CLI. Only text chat is supported.
