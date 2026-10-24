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
