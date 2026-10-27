# Verification record

Checks ran on 20 October 2026 on Apple silicon, macOS 26.5.1, Go 1.27.1 and Swift 6.2.3. The deployment target is macOS 15. Earlier macOS versions and Intel builds were not exercised.

## Executed checks

| Check | Result | Evidence |
| --- | --- | --- |
| Complete Go race suite | Passed. | verification-output.txt and package tests. |
| Go vet | Passed. | scripts/verify.sh. |
| MyGo native UI vet | Passed through the pinned framework command. | scripts/verify.sh. |
| Swift helper debug and release compilation | Passed. | build-output.txt records the release packaging check. |
| App bundle and usage-description plist | Passed. | build-output.txt. |
| Ad hoc signature verification | Passed for helper and complete bundle. | scripts/build-macos.sh and build-output.txt. |
| Native UI tester | Passed for Record, History, Settings, active controls and the minimum viewport. | main_test.go and screenshots/native-recording.png. |
| Audio protocol, conversion, resampling and alignment | Passed. | internal/audio/pipeline_test.go. |
| Live overflow diagnostic delivery | Passed using a running fake helper. | internal/audio/capture_test.go. |
| Capture failure, pause, resume, interruption recovery and bounded stop | Passed using fake helpers and an injected transcription runner. | internal/app/service_test.go. |
| SQLite transaction rollback, deduplication, restart durability and ordered retry | Passed using real temporary databases. | internal/storage/sqlite_test.go. |
| Soniox handshake, binary PCM, partial/final assembly and empty-text finalization | Passed using real WebSocket framing over net.Pipe. | internal/transcription/soniox_test.go. |
| Temporary disk spool ordering | Passed. | internal/transcription/spool_test.go. |
| OAuth token refresh and client-scoped token persistence | Passed with injected secret storage and HTTP transport. | internal/auth/google_test.go. |
| Google ambiguous append recovery after SQLite restart | Passed. The mock commits a batch, returns 503, and reconciliation avoids another write. | internal/sync/googledocs_test.go. |
| Google document creation retry after restart | Passed. | internal/sync/googledocs_test.go. |
| Native device discovery | Initial discovery returned an empty list. Later discovery after the microphone-test change found a built-in microphone and a Zoom virtual device. | native/macos/.build/release/titen-audio --list-devices. |
| Local microphone test | Selected input arguments, source filtering, RMS level, cancellation, missing input and permission errors passed with fake helpers. | internal/audio/microphone_test.go. |
| Real microphone-only helper | A short attempt returned Microphone access denied, exit 1, and zero audio bytes. No further capture was attempted. | Native helper execution. |

The retained native screenshot uses synthetic meeting text in MyGo's tester. It is layout evidence, not a screenshot of live recording.

## Restrictions encountered

The managed environment denied writes to default Go and Clang module caches. Verification uses writable cache directories. Swift emits warnings about its user-level manifest cache, but the release build completes. A full go mod tidy tried to fetch an uncached upstream test-only dependency and could not resolve the network host; application builds and checks succeeded with the recorded module files.

Binding a host test port was denied. Provider integration tests therefore use an in-process HTTP RoundTripper and real WebSocket framing over net.Pipe. The production OAuth listener still uses a loopback TCP port, which requires a normal desktop environment.

Computer Use rejected inspection of build/Titen.app with the message "Computer Use was not approved to use Titen". No packaged-window interaction was claimed or attempted through another automation route.

## Acceptance gates still open

No actual microphone or system audio stream was recorded. A standalone microphone-only helper exercised the denied-permission error path. App-bundle permission prompts, permission revocation, device changes, headphone behavior, Meet, Zoom, Teams, and a 60-minute run remain unverified.

No real Soniox credentials or Google OAuth grants were exercised. Live Indonesian and English transcription, long-session provider rotation, real Google document writes, and recovery against real providers remain unverified. Native Keychain code compiled and linked; no real credentials were saved by the tests.

Unconfirmed audio can be lost after a provider disconnect. The temporary spool preserves unsent audio only while the process lives, with a 1 GiB total limit. Stop allows up to 15 seconds for finalization and may leave speech missing after a longer outage. Named ranges must remain intact for append reconciliation. Document creation can leave an orphan document after an ambiguous success because the Docs create API does not use the append markers. Existing arbitrary documents require an app authorization mechanism such as a picker, which is not included.

Apple Developer ID distribution signing, notarization, Windows capture, and Linux support were not included in the original 20 October verification. That run did not create a commit, push, or PR.

Follow manual-testing.md to close the live acceptance gates. The PRD's primary end-to-end success criterion is NOT VERIFIED.

## Persistent signing follow-up

On 24 October 2026, the Apple silicon local signing check built Titen twice in sequence using the persistent self-signed certificate. After build 1 and again after build 2, the packaged helper's native permission check reported Microphone and Screen Recording authorized. The check also compared the app/helper bundle IDs, signer fingerprint, designated requirements, and entitlements; each value matched across both builds. No TCC entry was reset during this check.

A separate package run imported the same PKCS#12 into a temporary keychain, built and signed the app, and passed `cmd/packagecheck` on the resulting macOS ARM64 ZIP and SHA256. This exercises the temporary-keychain path used by GitHub Actions locally. The workflow has not been run on GitHub with repository secrets, and no public GitHub Release was published. These checks do not prove live microphone or system-audio capture on another Mac. Apple Developer ID signing and notarization remain out of scope.

## Linux follow-up

On 27 October 2026, Linux AMD64 support was built and checked on Linux Mint 22.3 (Ubuntu 24.04 derivative). The race suite, vet, helper self-test, packaging, archive and Debian package validation, Secret Service roundtrip, and packaged startup all passed. The Debian package places the app under `/opt/titen` with its helper, desktop entry, and `titen` command, and declares GTK 3, `pulseaudio-utils`, and `libsecret-1-0`. The extracted package and the portable app both found the helper and listed three microphone sources. The Ubuntu 24.04 CI job passed, and packagecheck verified the downloaded artifact's archive and Debian package. Live capture was not started, so microphone and playback recording remain unverified. See [cross-platform verification](cross-platform/verification.md) for the checks and open boundaries.
