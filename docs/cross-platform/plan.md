# Cross-platform implementation plan

## Workflow

- [x] Read the Principles section of poteto-mode.
- [x] Frame.
- [x] Design the workflow.
- [x] Run the loop.
- [x] Keep the audit trail.
- [x] Verify and hand back.

## Done predicate

macOS and Windows builds include a native capture helper, private SQLite persistence, secure credential storage, and native UI. A pushed version tag runs native tests, packages the app and helper, and exposes downloadable archives with checksums through GitHub Actions. A manual dispatch and pull request checks exercise the same build before any release tag is created. Hardware capture and notarization remain explicit acceptance gates.

## Grounding and design

The initial commit is 825533a. The existing macOS verification script passes. There is now a GitHub remote at nggalek-co/titen and no existing Actions workflow.

Two designs were compared. Keeping native helpers preserves the existing TIT1 frame protocol and separates native failures from Go. Moving capture into Go removes process packaging but expands COM and Swift ABI ownership. Keep native helpers. Windows uses C++ WASAPI; macOS keeps Swift. A stdin stop request replaces Unix-only parent signals.

SQLite alternatives were bundled Cgo SQLite and modernc SQLite through database/sql. Select modernc to remove the Windows SQLite compiler and DLL dependency while preserving the existing SQLite schema. Retain macOS Keychain and add Windows Credential Manager.

## Throughput checkpoint

- [x] Blocking first steps. Inspect GitHub, run the baseline, read pinned MyGo source, and compare native-helper and in-process designs.
- [x] Independent workstreams. Windows owns native/windows. Persistence owns storage, credential implementations, and Go module dependencies. Lifecycle owns helper cancellation, portable test fixtures, and Swift shutdown. The coordinator owns main integration, build scripts, workflows, and documentation.
- [x] Shared mutable state. Only persistence edits go.mod and go.sum. Writers own disjoint files. The coordinator integrates after each package contract is confirmed.
- [x] Smallest safe decomposition. Three code workers cover separate platform boundaries. One coordinator owns release packaging and native CI proof.

## Checks

- [x] Preserve existing database files and transactional outbox behavior.
- [x] Compile Windows packages without Cgo.
- [x] Run lifecycle tests on Windows and macOS without a shell fixture dependency.
- [x] Build Swift and Windows WASAPI helpers.
- [x] Run Windows helper conversion and frame self-tests without recording.
- [x] Build macOS ARM64, macOS Intel, and Windows AMD64 archives containing app and helper.
- [x] Run GitHub Actions on the feature branch and inspect downloadable artifacts.
- [x] Review code, workflow permissions, packaging, and acceptance limits.

The web UI and SaaS backend are implemented in [titen-web](../../../titen-web), which owns [PRD 002](../../../titen-web/docs/prd/002.md). This work ports the existing desktop application and release process. The desktop integration lives in internal/saas and uses the server endpoint saved in Settings.
