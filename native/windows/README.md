# Windows audio helper

`titen-audio.exe` captures the selected microphone and the default playback device through WASAPI shared mode. It requests event-driven buffers for microphone capture and render-endpoint loopback, then drains each ready packet on its own capture thread and COM apartment. A bounded queue sends complete TIT1 packets through one stdout writer.

Build with Visual Studio 2022 C++ tools and the Windows SDK.

```powershell
cmake -S native/windows -B build/windows-audio -A x64
cmake --build build/windows-audio --config Release
ctest --test-dir build/windows-audio -C Release --output-on-failure
```

The executable is `build/windows-audio/Release/titen-audio.exe`.

## Command contract

- No arguments capture both sources.
- `--microphone-only` captures the microphone.
- `--system-only` captures playback through loopback.
- `--microphone <endpoint-id>` selects a microphone returned by `--list-devices`.
- `--list-devices` prints a JSON array with `id` and `name` fields.
- `--self-test` checks conversion and the TIT1 wire format without audio hardware.

The parent keeps stdin open while recording. A newline or EOF stops both sources. The helper also handles console termination. Diagnostics use stderr. Device invalidation, default-device changes, and capture errors stop both sources and return a nonzero exit code. When Windows denies microphone capture, the error points to the desktop-app microphone toggle in Privacy settings.

## Audio contract

Packets use the same 24-byte little-endian TIT1 header as the macOS helper. Source 1 is the microphone and source 2 is playback. The payload is mono signed PCM16 at the device sample rate. The helper downmixes at most eight channels and accepts sample rates between 8 kHz and 192 kHz. The Go pipeline performs resampling.

Timestamps use the WASAPI QPC position in nanoseconds, so both capture threads share one clock. Silent WASAPI packets become zero-valued PCM. The writer queue holds at most 128 packets. Overflow drops the oldest packet and emits a warning.

## Verification limits

`--self-test` runs on macOS and Windows. It checks float conversion, stereo downmix, PCM24, PCM32, silence, and a complete binary packet fixture. It does not prove live capture. Windows hardware acceptance must verify microphone permissions, simultaneous loopback, device changes, and clean shutdown.
