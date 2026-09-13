# CPAMC Chrome installation probe (experimental)

This directory is a **bounded experiment**, not a Chrome installer and not a production extension. It generates an inert, permission-free MV3 CRX3 package and a user-scoped macOS Chrome policy profile that allowlists only that package's generated extension ID and one nonce URL path.

## What it can test

1. Run `node installation-lab/build.mjs --build` (Node 22+, system `zip`). The generated files are written only to `artifacts/installation-probe/`:
   - `probe.crx` — inert MV3 extension;
   - `chrome-installation-probe.mobileconfig` — `ExtensionInstallSources` for `http://127.0.0.1/cpamc-install/<nonce>/*` and an exact-ID `installation_mode: allowed` rule;
   - `index.html` — explicit user-action page;
   - `metadata.json` — generated ID, nonce, source pattern, and output paths;
   - `private-key.pem` — signing key, mode `0600`, reused to keep the ID stable.
2. The user must review and manually install the `.mobileconfig` profile at action time. This changes Chrome policy for the macOS user and can affect all Chrome profiles for that user; it is not silently applied and is not isolated to CPAMC.
3. Run `node installation-lab/build.mjs --serve --output artifacts/installation-probe`. The server binds only to `127.0.0.1` and serves only the generated nonce's `index.html` and `probe.crx` routes.
4. Open the printed `index.html` URL and press **Install extension probe**. This is the only install attempt. Chrome may show its normal confirmation UI; if it downloads the file instead, no installation happens automatically and the result must be reviewed before continuing.

The policy does **not** force-install the extension, allow all origins, or grant any extension permission. The `.mobileconfig` applies administrative Chrome policy to the current macOS user without enrolling the device in MDM. Chrome can display “managed by your organization” while it is installed. The probe has no background page, content scripts, host permissions, cookies permission, storage permission, or CPAMC integration. It cannot read accounts or cookies.

## Checks

`node installation-lab/test.mjs` builds into a temporary directory and independently checks the CRX3 RSA-PKCS#1-SHA-256 signature, extension ID and archive allowlist, XML policy keys, and exact server routes. It also checks malformed-token, traversal, and non-GET requests. No network or Chrome process is required.

This experiment cannot guarantee that current Chrome versions will show an installation prompt. Chrome policies, browser channel, enterprise controls, macOS profile approval, and off-store installation rules remain external gates. It does not modify Chrome or apply profiles automatically.

## Observed local result — 2026-09-12

With explicit user consent, the inert probe was tested in official Google Chrome **152.0.7977.83** on **macOS 26.6.2 arm64**:

- Before testing, System Settings showed no installed profiles and Chrome showed no policies.
- The profile was installed through System Settings, not by editing managed preferences.
- Both policies appeared in `chrome://policy` as **Platform / Current user / Mandatory / OK**.
- Clicking the local page link opened Chrome's native **Add “CPAMC Installation Probe”?** dialog.
- After acceptance, the extension details showed **On, extension enabled**, **Not from Chrome Web Store**, no special permissions or site access, with **Developer mode off**.
- Cleanup removed only the probe extension and its profile. System Settings again showed no profiles and Chrome showed no policies after reload. The local test server was stopped.

This verifies the installation flow for that inert package and environment, not the production Web Accounts extension, browser-restart persistence, updates, account inference, Windows or Linux. The profile approval remains a separate one-time OS step; this is not zero-friction installation from a website alone. No CPAMC source or account credentials were changed.
