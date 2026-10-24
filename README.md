# Titen

Titen is a native desktop meeting transcription application for macOS and Windows built with MyGo native GPU widgets, Go, and platform-specific native capture helpers. It captures microphone and playback audio, streams mixed speech directly to Soniox with a temporary key issued by Titen, and saves finalized transcripts in SQLite.

The macOS application builds locally. Windows builds and tests run on GitHub-hosted Windows runners. Live capture and hosted provider acceptance remain unverified in this environment. Read [verification results](docs/verification.md) before relying on it for a meeting.

The web UI and SaaS backend are implemented separately in [titen-web](../titen-web), which also owns [PRD 002](../titen-web/docs/prd/002.md). It contains account management, prepaid billing, device authorization, transcription sessions, and Google integration. The desktop signs in through the web account, requests temporary Soniox credentials, and uploads finalized transcript segments through authenticated heartbeats. See [direct streaming](docs/saas-integration/direct-soniox.md) for the matching backend contract and billing limits.

## Build and run on macOS

You need macOS 15 or later, Xcode Command Line Tools with Swift, OpenSSL 3, and Go 1.27.1 or later. The native build does not need Node, Bun, Vite, or a web server.

```sh
sh scripts/build-macos.sh
open build/Titen.app
```

The script builds the Swift helper and Go application, creates the app bundle, and signs the app and helper with the persistent `Titen Self-Signed Code Signing` identity. The first build creates the self-signed certificate in your login keychain, stores the PKCS#12 file under `~/Library/Application Support/Titen/Signing/`, and keeps its passphrase in the login Keychain. Later builds reuse it. The bundle contains both executables under Contents/MacOS. Keep them together.

This does not notarize the app. A first launch on another Mac can still show Gatekeeper's unidentified developer warning. The stable certificate, bundle IDs, designated requirements, and entitlements let macOS match TCC permissions across rebuilds. The same certificate should be reused for every tagged build.

For development, build the helper first with the build script, then run `go run .` from the project root. The app locates the release helper under native/macos/.build/release when it is not beside the Go executable. The packaged app gives macOS a stable bundle identity for permission checks.

## Windows builds

Windows 10 and later on x64 are the initial Windows target. Build with Go 1.27.1 or later, CMake, and Visual Studio 2022 or later with the Desktop development with C++ workload and Windows SDK.

```powershell
./scripts/build-windows.ps1
```

The build produces a portable ZIP and a per-user installer in `dist/windows-amd64`. The portable app is staged at `build/windows/Titen`; keep `Titen.exe` and `titen-audio.exe` together. The installer uses MyGo's stable app identity and installs under `%LOCALAPPDATA%\Programs\Titen`; it does not need administrator access. Settings, credentials, logs, and transcripts stay under `%APPDATA%\Titen` across upgrades.

Windows capture uses shared-mode WASAPI event notifications for microphone input and playback loopback. If Windows denies microphone access, open **Settings → Privacy & security → Microphone**, enable microphone access and **Let desktop apps access your microphone**, then retry. Titen's microphone settings button opens that page. Loopback reads the selected playback endpoint and has no separate microphone permission prompt.

The MyGo native updater checks for releases in the background and offers updates from Titen's tray menu. Update archives are signed with a persistent Ed25519 key whose public half is in `mygo.json`; this provides update authenticity without a paid code-signing certificate. The Windows installer and app executables remain unsigned. SmartScreen can warn on each new installer, and Smart App Control or organization policy may block unsigned apps. Download only from the official Titen GitHub release and verify its SHA256 before deciding whether to continue. The MyGo update signature does not remove Windows' unsigned-app warning.

The CI upgrade check installs versions 0.0.1 and 0.0.2 in sequence and verifies that the app version changes while `%APPDATA%\Titen\settings.json` remains byte-for-byte intact.

The native helper has a hardware-free protocol and conversion test.

```powershell
./build/windows-audio/Release/titen-audio.exe --self-test
```

The first supported release targets macOS ARM64, macOS Intel, and Windows AMD64. Linux and Windows ARM64 capture are not included. Physical Windows capture remains a separate acceptance test from compilation and protocol tests.

## GitHub builds on version tags

The Desktop builds workflow runs native checks and packages pull requests, pushes to main, manual dispatches, and tags matching `v*`. A successful tag run also publishes a GitHub Release. For example, pushing `v1.2.3` builds that exact tagged source.

Each successful run uploads three downloadable GitHub Actions artifacts. The Windows artifact also contains the per-user installer and, for tagged builds, MyGo's signed updater manifest and archive. A tag run publishes these assets alongside the platform ZIPs and checksums.

- `Titen-macos-arm64` contains the Apple silicon app and Swift helper.
- `Titen-macos-amd64` contains the Intel app and Swift helper.
- `Titen-windows-amd64` contains the Windows application and WASAPI helper.

Download CI artifacts from the workflow run's Artifacts section. Version tags set the embedded application version and archive filenames. A failed platform build blocks publication of the release.

macOS packages on branches, pull requests, and manual runs use an ephemeral self-signed identity for build validation. Tagged macOS builds require the shared signing certificate secrets described below. Tagged Windows builds require the persistent MyGo update key. Windows executables and installers are unsigned, and MyGo's update signing does not act as Windows code signing. The self-signed macOS packages are not notarized, so Gatekeeper can still warn on first launch.

### Persistent macOS signing in GitHub Actions

Create the local certificate once, then store that exact PKCS#12 file and password as repository secrets. This keeps the CI signer consistent with local builds and across tags:

```sh
sh scripts/sign-cert.sh
signing_dir="$HOME/Library/Application Support/Titen/Signing"
base64 < "$signing_dir/titen-self-signed.p12" | tr -d '\n' | gh secret set TITEN_SIGNING_CERTIFICATE
swift scripts/keychain-password.swift get co.nggalek.titen.codesign.p12 'Titen Self-Signed Code Signing' | gh secret set TITEN_SIGNING_CERTIFICATE_PASSWORD
```

### Persistent Windows update key

MyGo's Ed25519 update key is separate from Windows Authenticode signing. The private key must stay outside the repository and remain backed up; installed Titen builds only accept updates signed by that key. The release workflow reads it from the `MYGO_UPDATER_PRIVATE_KEY` repository secret:

```sh
gh secret set MYGO_UPDATER_PRIVATE_KEY < path/to/mygo-update.key
```

Use the existing key that matches `updates.publicKey` in [mygo.json](mygo.json). Do not generate a replacement for routine releases. Local Windows builds need no secret; local builds that should emit signed update assets can set `MYGO_UPDATER_PRIVATE_KEY` from the same private key file.

The private key stays in the PKCS#12 file and local Keychain. The passphrase is stored in the macOS login Keychain instead of a plaintext file. The two GitHub repository secrets are encrypted by GitHub and available to Actions; Base64 only encodes the PKCS#12 for transport. Keep both secrets unchanged for every release. No paid Apple Developer account is used. The version-tag workflow imports the identity into a temporary keychain, signs both executables and the app, validates the designated requirements and entitlements, and removes the temporary keychain after packaging.

See [cross-platform architecture and verification](docs/cross-platform/README.md).

## Connect the desktop to Titen

Production builds default to `https://titen.nggalek.co`; local development defaults to `http://localhost:3000`. Open **Settings** to choose another server endpoint. Hosted endpoints must use HTTPS. Save settings, then click **Sign in with browser** and approve the displayed code. The desktop stores its device token in Keychain or Windows Credential Manager, scoped to that endpoint.

Start a meeting as usual. Titen reserves account credit before capture, sends 16 kHz mono audio directly to Soniox, and saves finalized transcript segments locally. The backend retains the long-lived Soniox key and issues a single-use temporary key for each recording session. It reconciles charges from provider usage records. Unused reserved credit remains held until reconciliation. Open **Open billing** or **Google settings** in the browser to manage account credit and Google access.

The `server_endpoint` setting is stored in the local `settings.json` file. Device tokens remain in the operating system credential store.

## Record a meeting

On Windows, enable desktop microphone access in Windows Settings under Privacy & security > Microphone. System audio uses the default playback endpoint.

Approve microphone and Screen & System Audio Recording permission when macOS requests them. If access fails, open **System Settings > Privacy & Security**, grant access to Titen, and start again.

The main window shows recording state, elapsed recording time, separate audio meters, finalized text, and partial speech. The live view retains the latest 500 finalized segments. Complete transcripts remain in **History**.

Closing the window hides it. Recording continues. The menu bar provides start, pause, resume, stop, open, and quit actions. Quit ends capture and allows up to 15 seconds for final transcription before leaving.

Pause terminates capture. To change microphones, pause, open **Settings**, refresh the microphone list, choose a device, save, and resume. Source modes support both inputs, microphone only, and system audio only. On macOS, microphone-only mode uses AVFoundation and requires microphone permission only. Windows uses WASAPI.

In **Settings**, click **Test microphone for 10 seconds** to check the selected input before recording. The live test meter shows microphone input only. **Stop microphone test** ends it early. This test creates no meeting, writes no audio file, and makes no Soniox or Google request. It requires microphone access only.

Use headphones to reduce playback leakage into the microphone. Titen does not provide acoustic echo cancellation. System playback capture does not require a virtual loopback driver.

## Read transcripts locally

Open **History**, search meeting titles or transcript text, and select a meeting. Use **Copy transcript** or **Open Titen meeting** to view the server copy and Google Docs.

Data lives in `~/Library/Application Support/Titen/` on macOS and `%AppData%\Titen\` on Windows. SQLite contains local meetings, speaker and timestamp metadata, and finalized segments. The settings file contains the endpoint and audio preferences. Back up the whole directory while Titen is stopped if you need a consistent manual copy. Device credentials are separate from that directory.

## Understand recovery limits

Capture starts as soon as **Record** is clicked, before the server authorization request finishes. Titen buffers about 25 seconds of startup audio while it waits for that request. During a running session, unsent PCM is buffered in a private temporary disk file (1 GiB maximum). The UI shows whether transcription is connecting, connected, or reconnecting; it also reports when capture is being restarted.

Closing the window only hides Titen. Unexpected audio-helper exits trigger automatic capture retries with increasing delays until capture is available or you stop the session. The computer cannot record while asleep; speech during sleep is missing, and the meeting is marked incomplete even if capture resumes after wake. Check microphone and screen-audio permissions if recovery does not succeed.

After a Soniox disconnect, Titen keeps recording and buffers unsent audio while it requests a fresh grant for the same meeting. The server can reject that renewal with HTTP 409 until the existing grant is within its renewal window (up to about 9.5 minutes for the current 10-minute grant). A successful WebSocket write has no delivery acknowledgement, so a chunk at the moment of disconnect can be missing or duplicated. The 1 GiB spool is deleted at session cleanup and cannot recover audio after an application crash.

Finalized transcript segments are saved locally as they arrive and survive an app restart. Startup marks a meeting interrupted if the app exits before the session finishes. Unfinalized speech and the temporary audio spool do not survive a crash. Stop allows up to 15 seconds for final transcription; a longer outage can leave speech missing.

This spool is a recovery buffer, not a full audio recording archive.

## Verify changes

```sh
sh scripts/verify.sh
sh scripts/build-macos.sh
```

Tests use real temporary SQLite databases, real WebSocket framing over in-memory connections, HTTP transports, fake capture executables, and MyGo's native UI tester. They do not upload audio or write to real Google accounts.

See [architecture](docs/architecture.md), [verification results](docs/verification.md), and the [manual macOS acceptance procedure](docs/manual-testing.md).

## Troubleshoot

- Startup checks inspect the selected audio mode and account access. Recording and resume stay disabled until those checks pass. Use **Request access** to show macOS permission prompts, then **Recheck** after changing permissions. Settings, logs, and stop controls remain available.
- Local macOS builds create and reuse the persistent self-signed identity automatically. If the certificate is missing from the login keychain, run `sh scripts/sign-cert.sh`; do not delete the signing files under `~/Library/Application Support/Titen/Signing/` or the passphrase item in the login Keychain, or replace the certificate used by GitHub Actions. A change of signer can require granting TCC permissions again.
- In **Settings → Open logs**, locate `titen.log` and attach it when reporting a problem. Logs persist across app restarts in the Titen config directory (`~/Library/Application Support/Titen/logs/` on macOS). Each line is a JSON event written with Go's `log/slog`, including timestamps and audio helper failure details. Audio samples and transcript content are not logged.
- If the helper cannot start, rebuild the bundle and check its path in **Settings**.
- If either meter stays at zero, check permission, source mode, microphone selection, and whether the meeting is producing playback audio.
- If Keychain access fails, approve access for the locally signed app. Rebuilding with a different signing identity can change Keychain authorization behavior.
- If a build reports a module-cache permission error, use the build script. It places Go, Swift, and Clang caches in the writable project cache directory.
- If a transcript is incomplete after an outage, inspect the visible error before starting another session. Unfinalized audio cannot be reconstructed from finalized text.

### macOS permission enabled, but startup check still fails

An enabled **Screen & System Audio Recording** toggle can refer to an older ad hoc signed build. Logs may report `com.apple.ScreenCaptureKit.SCStreamErrorDomain (-3801): The user declined TCCs for application, window, display capture` even though the toggle is on.

To refresh access for the current build:

1. Stop any active recording and quit Titen completely using **Titen > Quit Titen**. Closing the window only hides it.
2. Open **System Settings > Privacy & Security > Screen & System Audio Recording**.
3. Select the Titen entry and remove it with the minus button.
4. Click the plus button and select the exact app bundle you intend to run, such as this repository's `build/Titen.app`.
5. Ensure its toggle is on, authenticate if requested, and choose **Quit & Reopen** if offered. Otherwise, launch that app bundle yourself.
6. Confirm the startup warning disappears and **Start recording** becomes enabled. Use **Recheck** if needed. This verifies the permission check; verify live capture separately before a meeting.

To verify that microphone and Screen Recording access survive rebuilds, run the macOS check below. It requests any missing permission once, then builds twice and compares the signed IDs, certificate fingerprint, designated requirements, and entitlements while checking access through the same native APIs Titen uses:

```sh
sh scripts/verify-macos-signing.sh
```

The app bundle ID is `co.nggalek.titen`; the audio helper ID is `co.nggalek.titen.audio`. Their designated requirements use those IDs and the stable certificate common name. The checked-in `packaging/macos/Titen.entitlements` file is intentionally empty because the app is not sandboxed; microphone and Screen Recording access are governed by TCC prompts and the usage strings in `Info.plist`. Do not change these values casually. Apple Developer ID signing and notarization would still be required to remove Gatekeeper's first-launch warning.
