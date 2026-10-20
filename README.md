# Titen

Titen is a native desktop meeting transcription application for macOS and Windows built with MyGo native GPU widgets, Go, and a Swift ScreenCaptureKit helper. It captures microphone and playback audio, streams mixed speech to Soniox, saves finalized transcripts in SQLite, and synchronizes them to Google Docs.

The macOS application builds locally. Windows builds and tests run on GitHub-hosted Windows runners. Live capture and provider acceptance remain unverified in this environment. Read [verification results](docs/verification.md) before relying on it for a meeting.

## Build and run

You need macOS 15 or later, Xcode Command Line Tools with Swift, and Go 1.27.1 or later. The native build does not need Node, Bun, Vite, or a web server.

```sh
sh scripts/build-macos.sh
open build/Titen.app
```

The script builds the Swift helper and Go application, creates the app bundle, and signs it ad hoc for local use. Developer ID signing and notarization are not included. The bundle contains both executables under Contents/MacOS. Keep them together.

For development, build the helper first with the build script, then run `go run .` from the project root. The app locates the release helper under native/macos/.build/release when it is not beside the Go executable. The packaged app gives macOS a stable bundle identity for permission checks.

## Windows builds

Windows 10 and later on x64 are the initial Windows target. Build with Go 1.27.1 or later, CMake, and Visual Studio 2022 or later with the Desktop development with C++ workload and Windows SDK.

```powershell
./scripts/build-windows.ps1
```

Run `build/windows/Titen/Titen.exe`. Keep `titen-audio.exe` beside it. The package uses WASAPI microphone and playback loopback capture and Windows Credential Manager. No WebView2, external SQLite DLL, or virtual audio driver is required by this build.

The native helper has a hardware-free protocol and conversion test.

```powershell
./build/windows-audio/Release/titen-audio.exe --self-test
```

The first supported release targets macOS ARM64, macOS Intel, and Windows AMD64. Linux and Windows ARM64 capture are not included. Physical Windows capture remains a separate acceptance test from compilation and protocol tests.

## GitHub builds on version tags

The Desktop builds workflow runs native tests and creates archives on pull requests, pushes to main, manual dispatch, and pushed tags matching `v*`. For example, pushing `v1.2.3` builds that exact tagged source.

Each successful run uploads three downloadable GitHub Actions artifacts. Each artifact contains its platform ZIP and SHA256 checksum.

- `Titen-macos-arm64` contains the Apple silicon app and Swift helper.
- `Titen-macos-amd64` contains the Intel app and Swift helper.
- `Titen-windows-amd64` contains the Windows application and WASAPI helper.

Download them from the workflow run's Artifacts section. Version tags set the embedded application version and archive filenames. The workflow does not create tags or publish GitHub Releases. A failed platform build fails its job and produces no successful package for that platform.

macOS packages are ad hoc signed for local testing. Windows packages are unsigned. Developer ID signing, notarization, and Windows signing require separate distribution configuration. Do not treat a downloadable archive as trusted production distribution.

See [cross-platform architecture and verification](docs/cross-platform/README.md).

## Configure transcription and Google Docs

1. Open **Settings** and enter your Soniox API key.
2. Create a Google Cloud desktop OAuth client. Enable the Google Docs API and configure the OAuth consent screen. Add your account as a test user if the consent screen is in testing.
3. Enter the desktop OAuth client ID and its client secret in **Settings**. Save the settings.
4. Click **Connect Google** and complete authorization in your system browser.
5. Return to **Record**, enter a meeting title, and click **Start recording**.

Keys, OAuth client secrets, access tokens, and refresh tokens use macOS Keychain or Windows Credential Manager. The client ID and audio preferences are ordinary local settings. `GOOGLE_CLIENT_SECRET` optionally overrides the saved OAuth client secret for development. Environment variables set in a terminal do not automatically reach an app launched from Finder.

OAuth requests the [drive.file scope recommended by Google](https://developers.google.com/workspace/docs/api/auth). An optional existing document ID must refer to a document already authorized for this app. Pasting an arbitrary document ID does not grant access. A Drive picker is not included. Leave the ID empty to create a new document automatically.

Document creation and transcript synchronization retry independently of recording. Finalized text reaches SQLite before it can enter a Google Docs batch. Titen checks document access before attachment. Existing documents cannot be reassigned once attached to a meeting because the checkpoints belong to that document. An ambiguous successful document-creation response can leave an orphan document; append reconciliation prevents duplicate text in the attached document.

## Record a meeting

Approve microphone and Screen & System Audio Recording permission when macOS requests them. If access fails, open **System Settings > Privacy & Security**, grant access to Titen, and start again.

The main window shows recording state, elapsed recording time, separate audio meters, finalized text, partial speech, and cloud sync status. The live view retains the latest 500 finalized segments. Complete transcripts remain in **History**.

Closing the window hides it. Recording continues. The menu bar provides start, pause, resume, stop, open, and quit actions. Quit ends capture and allows up to 15 seconds for final transcription before leaving.

Pause terminates capture. To change microphones, pause, open **Settings**, refresh the microphone list, choose a device, save, and resume. Source modes support both inputs, microphone only, and system audio only. Microphone-only mode uses AVFoundation and requires microphone permission only.

In **Settings**, click **Test microphone for 10 seconds** to check the selected input before recording. The live test meter shows microphone input only. **Stop microphone test** ends it early. This test creates no meeting, writes no audio file, and makes no Soniox or Google request. It requires microphone access only.

Use headphones to reduce playback leakage into the microphone. Titen does not provide acoustic echo cancellation. System playback capture does not require a virtual loopback driver.

## Read transcripts offline

Open **History**, search meeting titles or transcript text, and select a meeting. Use **Copy transcript** or **Open Google Doc**. If automatic document creation is disabled, you can attach an app-authorized document ID later.

Data lives in `~/Library/Application Support/Titen/` on macOS and `%AppData%\Titen\` on Windows. SQLite contains meetings, speaker and timestamp metadata, finalized segments, and the durable synchronization outbox. The settings file contains preferences. Back up the whole directory while Titen is stopped if you need a consistent manual copy. Keychain credentials are separate from that directory.

## Understand recovery limits

Unsent audio is buffered in a private, unlinked temporary disk file during an active session. The total spool limit is 1 GiB. It is removed at session cleanup and cannot recover audio after an application crash. Audio already sent without confirmation can be lost after a provider disconnect. The UI reports disconnections and buffer overflow.

Native stream interruptions trigger at most three capture restarts within the same meeting. Permission failures require correction in System Settings. A temporary Google outage retains document creation requests and transcript batches across application restarts. Ambiguous append responses are reconciled using atomic named ranges and revision checks. Keep the app's named ranges in the document intact.

Stop allows 15 seconds to finalize transcription. A longer outage can leave unfinalized speech missing. Completed local transcripts remain available even when Google synchronization fails. Neither the SQLite outbox nor the temporary audio spool is a full audio recording archive.

## Verify changes

```sh
sh scripts/verify.sh
sh scripts/build-macos.sh
```

Tests use real temporary SQLite databases, real WebSocket framing over in-memory connections, HTTP transports, fake capture executables, and MyGo's native UI tester. They do not upload audio or write to real Google accounts.

See [architecture](docs/architecture.md), [verification results](docs/verification.md), and the [manual macOS acceptance procedure](docs/manual-testing.md).

## Troubleshoot

- If the helper cannot start, rebuild the bundle and check its path in **Settings**.
- If either meter stays at zero, check permission, source mode, microphone selection, and whether the meeting is producing playback audio.
- If Google returns 403, verify the Docs API, consent test user, and per-document authorization. A successful Google browser login alone does not prove document access.
- If Keychain access fails, approve access for the locally signed app. Rebuilding with a different signing identity can change Keychain authorization behavior.
- If a build reports a module-cache permission error, use the build script. It places Go, Swift, and Clang caches in the writable project cache directory.
- If a transcript is incomplete after an outage, inspect the visible error before starting another session. Unfinalized audio cannot be reconstructed from finalized text.
