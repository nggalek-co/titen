# Cross-platform verification

## Local checks

The initial macOS race suite and native UI vet passed before changes. After the port, the complete macOS race suite, Go vet and MyGo UI vet passed. SQLite tests read a native legacy fixture and preserve transcript, outbox, settings, and 64-bit timestamps.

Windows application cross-compilation with GOOS=windows GOARCH=amd64 CGO_ENABLED=0 passed. This is compilation evidence only. The portable Windows audio conversion and packet self-test also passed under the local C++ compiler, which does not compile Windows SDK branches.

The local macOS ARM64 app and Swift helper compiled, passed ad hoc signature validation, and were packaged into a ZIP. cmd/packagecheck opened the ZIP, verified both native Mach-O binaries and executable permissions, and matched the SHA256 file. Actionlint v1.7.12 validated the workflow.

## GitHub native checks

Native checks run in [Desktop builds](https://github.com/nggalek-co/titen/actions/runs/39511605664) for PR 1. The first native run passed the macOS ARM64 job and exposed a Windows spool failure. Windows forbids removing the default temporary-file handle while it is open. The fix uses a delete-on-close Windows handle and adds an executable cleanup test. A native rerun is pending at this checkpoint. The workflow uses the same tests and package commands on pushed version tags.

The branch was pushed through the existing SSH identity. HTTPS authentication could read the repository but lacked the workflow scope required to push an Actions file. No token scopes or credential configuration were changed.

## Unverified boundaries

No real microphone or system audio was recorded during this task. Hosted builds and protocol self-tests do not prove Windows hardware capture, device switching, long-running meetings, provider responses, or Google Docs delivery. The Windows minimum version needs physical Windows 10/11 testing. Native CI uses a Windows Server 2025 host.

macOS packages use local ad hoc signing. Windows packages are unsigned. Notarization, production signing, Linux capture, Windows ARM64 capture, and the SaaS backend are not part of this change. No release tag was created, and no public GitHub Release was published.
