# Cross-platform verification

## Local checks

`go test ./...` passed 86 tests across 14 packages. `go vet ./...`, MyGo UI vet, Windows AMD64 cross-compilation, and workflow YAML parsing passed.

On Apple silicon, `scripts/verify-macos-signing.sh` built the app twice with the persistent self-signed identity. Bundle IDs, signer fingerprint, designated requirements, and entitlements matched. Microphone and Screen Recording both remained authorized after build two.

## GitHub checks

[Desktop builds run 40076453477](https://github.com/nggalek-co/titen/actions/runs/40076453477) passed all three native jobs: macOS ARM64, macOS Intel, and Windows AMD64. The Windows runner compiled the WASAPI helper, passed its hardware-free CTest, packaged the app and installer, and upgraded from installer 0.0.1 to 0.0.2 while preserving `%APPDATA%\Titen\settings.json` byte-for-byte.

The pull request build intentionally has no update-signing key, so it does not create signed MyGo updater assets. The `v0.0.1` tag run will verify that secret-backed release path and publish the GitHub Release.

## Unverified boundaries

No real Windows microphone or system-audio stream was recorded. Hosted compilation and protocol tests do not prove hardware capture, endpoint changes, microphone privacy prompts, headphones, or long meetings. Physical Windows 10/11 testing remains open. Provider and Google Docs acceptance were not part of these checks.

Windows installers and executables remain unsigned; SmartScreen may warn, and the MyGo Ed25519 update signature is not Authenticode signing. macOS packages are self-signed and not notarized.

## Linux checks

`sh scripts/verify.sh` passed the race suite, `go vet ./...`, and MyGo UI vet on Linux Mint 22.3 (Ubuntu 24.04 derivative, PipeWire 1.0.5 through PulseAudio compatibility). The new `native/linux` package tests framing against `internal/audio.ReadFrame`, monitor filtering, and the helper self-test. `cmd/packagecheck` tests cover a Linux archive with a missing helper.

`sh scripts/build-linux.sh` built the app and helper with `CGO_ENABLED=0`, ran `titen-audio --self-test`, staged `build/linux-amd64/Titen`, and wrote `dist/Titen-dev-linux-amd64.tar.gz` with a SHA256 file. `go run ./cmd/packagecheck -dir dist -os Linux -arch amd64` verified the archive, both ELF AMD64 binaries, executable permissions, and the checksum.

`titen-audio --list-devices` returned three microphone sources and omitted every `.monitor` source. With `TITEN_TEST_CREDENTIALS=1`, the Secret Service roundtrip and missing-key tests passed against the running keyring.

An early probe with a nonexistent `--microphone` source did not fail as expected: `parec` fell back and opened a short capture. Its output was discarded. The helper now checks the saved source through `pactl` before opening a stream, and the rejection was rechecked.

The packaged app launched from `build/linux-amd64/Titen`. Its log reported `audio startup check completed` with no error and `microphones discovered` with three devices, and its window rendered. That check did not start capture.

### Linux unverified boundaries

Live capture was not assessed: the short fallback capture above was discarded, and simultaneous sources, the selected device, default-sink changes, headphones, long meetings, and provider access were not exercised. The helper derives timestamps from a shared sample clock rather than server presentation time. The app captures only the default sink's monitor and does not follow a later default-sink change. The Ubuntu CI job has not run yet. Stock Ubuntu desktop runtime packages, non-GNOME Secret Service providers, and Linux ARM64 packaging remain untested.
