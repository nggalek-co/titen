# Ubuntu desktop support run

## Done predicate

- `scripts/build-linux.sh` creates a runnable Linux AMD64 app folder and checksum archive under `build/` and `dist/`.
- The packaged app launches on the current Ubuntu-based desktop and its startup check finds the packaged audio helper.
- The helper lists PulseAudio-compatible microphone sources and can select the default playback monitor while keeping the existing `TIT1` frame format.
- Linux credentials use the desktop Secret Service. The app does not write access tokens to its settings file.
- The Ubuntu build path and its runtime requirements are documented. CI checks the Linux package contents.

## Scope and limits

This run covers four boundaries: Linux audio capture, Secret Service credentials, local packaging, and build documentation and CI. The baseline Go app already compiles and opens on this Linux Mint 22.3 host. Its startup check fails because Linux has no packaged audio helper. Its credential backend also returns an unsupported-platform error. The host runs PipeWire 1.0.5 through PulseAudio compatibility and has `pactl`, `parec`, and `secret-tool` available.

The host is an Ubuntu 24.04 derivative, not stock Ubuntu. The build and desktop session can be checked here. Direct microphone or playback recording is out of scope for verification because it would capture live user audio. This run will verify helper discovery and app startup without starting capture.

## Work plan

- [x] Read the Principles section of the poteto-mode skill.
- [x] Phase A. Frame the observable failure, scope, and acceptance limits.
- [x] Phase B. Compare Linux audio and credential integration designs, then choose the smallest maintainable shape.
- [x] Phase C. Run these units in order and keep each check green before continuing.
  - [x] Add a Linux helper that lists sources and emits the existing `TIT1` frames from PulseAudio-compatible input streams.
  - [x] Add Linux Secret Service storage and a focused user-facing error when its runtime is unavailable.
  - [x] Add a rerunnable Linux build and packaging script, then inspect the archive contents.
  - [x] Add the Ubuntu build gate and document installation and runtime requirements.
- [x] Phase D. Keep the decision log current and review every entry against the run transcript.
- [x] Phase E. Build and launch the packaged app on the desktop, check helper discovery, inspect the final diff, and report any hardware checks that remain open.

## Design decision

Two audio designs were compared.

- A native C helper linked against libpulse. It would read server presentation
  timestamps and enumerate devices through the client API, but it adds a
  libpulse development package and a cgo toolchain to every Linux build. The
  host has no headers, and the helper would need its own context state machine
  and two capture threads.
- A Go helper over the PulseAudio-compatible command line tools. `pactl`
  reports sources as JSON, `parec` streams raw PCM from a named source or from
  `@DEFAULT_SOURCE@` and `@DEFAULT_MONITOR@`. The helper keeps the TIT1 frame
  format, the stdin stop contract, and the one-process boundary. It adds a
  runtime dependency on `pulseaudio-utils` and derives timestamps from a shared
  sample clock instead of server presentation time.

Choose the Go helper. It is the smallest shape that preserves the existing
helper contract, needs no compiler packages, and can be tested without audio
hardware. Both `parec` processes start from one wall-clock origin, so the Go
pipeline still aligns microphone and playback samples on a shared clock.

Three credential designs were compared: a direct Secret Service D-Bus client,
the `secret-tool` command, and `mygo.Secrets`. The first adds a D-Bus
dependency and the unlock flow. The second adds another runtime binary and
relies on exit codes. `mygo.Secrets` already wraps libsecret through purego,
returns `errors.ErrUnsupported` when no store exists, and lets the store pin
the `co.nggalek.titen` service used by macOS, Windows, and `mygo.json`.
Choose `mygo.Secrets`.

## Architecture checklist

- [x] Ground the current app, capture, and credential boundaries.
- [x] Sketch at least two complete Linux designs and compare their interfaces.
- [x] Proceed with the synthesized design. No approval checkpoint was requested.
- [x] Implement against the chosen shape and record any justified deviations.
- [x] Scrap and redesign only if implementation shows repeated friction the sketch cannot absorb.
