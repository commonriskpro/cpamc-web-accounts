# Validation — 2026-09-12

## Current result

Native **0.1.2** is loaded in the local macOS M backend (PID 622 unchanged).
CPAMC visibly shows **Cuentas web** below **Control Account**, and its native
resource page renders. All three plugins remain effective; six existing auth
files and 25 models remain unchanged. Upstream sources remain clean. The only
additional configuration change is a revision comment; prior bytes and old
libraries are preserved.

Extension **0.2.0** passes 23 tests and final independent review with no critical
findings. It is packaged locally, not published or installed in Chrome. Real
Chrome permissions/runtime, real-account inference and Linux/Docker runtime
are still pending. Earlier sections below are historical verification receipts.

Loaded native SHA256:
`3291ea12489ac002cda18754cda4c2b4d6e4e6c17660fbed651bd79af47c9a8e`.

## Initial installation (0.1.0)

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

## Follow-up: browser connector and native 0.1.1

- Chrome connector source, injectable browser-API tests: 12/12 PASS; independent
  corrected-source review PASS. UI preview used synthetic browser/server data.
- Direct controller integration with a freshly started isolated real backend:
  Claude/Kimi/Qoder uploads, unique records after watcher settling, models and
  disconnect PASS. No real-account credentials or inference were used.
- Native 0.1.1 fixes upload/watcher duplicate IDs by using the auth filename.
  Unit/race/vet and C ABI smoke PASS; macOS ARM64 and all four Linux builds PASS.
- Native versioned-path hot replacement: PASS after correcting the test to
  inspect `metadata.version` (not nonexistent top-level `version`) and changing
  config bytes with a comment. Identical config writes are skipped by the watcher.
  The live CPAMC UI now reports 0.1.1 Effective/Registered; lsof confirms the
  mapped 0.1.1 library. PID 622 remained unchanged. Old mapped libraries were
  retained. Prior config bytes were preserved with only a revision comment
  appended; six existing auth files and 25 API models remain unchanged.
  Loaded 0.1.1 macOS ARM64 SHA256:
  `9ec05996fb3042d0b8d1c4db346c586f4a60982d313e3bd9be834b3df5c06246`.
- The user's preferred integrated UX is now implemented in extension 0.2.0:
  a panel inside the existing CPAMC tab, explicit temporary XHR session reuse,
  no second Management-key form. The frontend and API must share one origin.
- Integrated extension tests: 19/19 PASS. The hidden synthetic browser fixture
  exercised serialized production injection functions and a real fixture XHR:
  open panel, reuse session, all three provider actions, disconnect, reconnect,
  close, and reopen without inheriting an old pairing: PASS.
- Browser extension installation, real Chrome permission/runtime behavior and
  live-provider inference remain unvalidated. No actual browser cookies or
  Management session have been captured by the new extension.

## Follow-up: integrated extension 0.2.0 correction and native menu 0.1.2

- Extension correction: close-during-pending-session-write now cleans only the
  cancelled capture's session; post-async-guard session checks prevent capture
  after disconnect. Toolbar reactivation cancels the previous capture.
  Tests: 23/23 PASS. Obsolete standalone `connect.*` forms were removed.
- Corrected UI fixture: pending capture → toolbar reactivation → immediate new
  pairing, synthetic Claude upload, disconnect and close during pending capture:
  PASS. This uses a real fixture XHR but synthetic Chrome APIs, not Chrome MV3.
  The fixture tab and server were closed after verification.
- Native 0.1.2 registers the public, static **Cuentas web** resource through
  `management.register`. No frontend credentials, scripts or forms are served.
- Native unit tests, race detector, vet and macOS ARM64 build: PASS. Docker
  build context explicitly includes the embedded HTML. Four Linux cross builds
  (amd64/arm64 × glibc/musl) PASS; Linux/Docker runtime remains untested.
- Isolated actual backend: versioned-path hot replacement to 0.1.2, native menu,
  unauthenticated static page/CSP/unknown-route rejection, three synthetic
  provider uploads, unique auth records after the watcher, model registration
  and disconnect: PASS. Existing/live account credentials were not used.

- Final independent integrated audit: PASS, no critical defects. Live CPAMC
  metadata confirms 0.1.2 Effective/Registered/Configured; sidebar and embedded
  page visually verified. Old libraries retained; backend PID 622 unchanged.
