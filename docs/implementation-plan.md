# Implementation checklist

- [x] Read the Principles section of poteto-mode.
- [x] Frame the success predicate and inspect the starter.
- [x] Design the workflow and compare architecture candidates.
- [x] Run the implementation and verification loop.
- [x] Keep the audit trail.
- [x] Verify the application and hand back precise remaining limits. Local gates passed. Live acceptance remains open in verification.md.

## Success predicate

A packaged native macOS application captures independent microphone and system audio, sends mixed PCM to Soniox, persists finalized text, and synchronizes it to Google Docs. Tests must prove local data durability and ambiguous retry safety. Live provider proof requires configured credentials and macOS capture permission. A sixty minute simultaneous capture is a separate acceptance gate.

## Throughput checkpoint

- [x] Blocking first steps. Read the PRD, installed MyGo source, and current official API documentation before implementation.
- [x] Independent workstreams. Audio owns internal/audio and native/macos. Cloud owns storage, transcript, transcription, auth, sync, and module dependencies. The coordinator owns application integration, native UI, packaging, and documentation.
- [x] Shared mutable state. Only the cloud worker edits go.mod and go.sum. UI state changes on the main thread. Background workers publish snapshots through Window.Update.
- [x] Smallest safe decomposition. Three workers cover capture, cloud, and independent design review. Integration stays with the coordinator because recording lifecycle couples these packages.

## Verification sequence

1. Compile Swift capture and test the binary frame protocol, mixer, resampler, and alignment.
2. Test SQLite restart durability, segment assembly, mock Soniox, and mock Google retry behavior.
3. Build the native UI and test recording lifecycle with injected sources.
4. Package the app and open its native window. Record actual permission and credential blockers.
5. Run the complete test suite and retain output in docs/verification.md.

There is no Git repository in this checkout. Commits, rebasing, and opening a pull request are skipped until a repository and remote exist.
