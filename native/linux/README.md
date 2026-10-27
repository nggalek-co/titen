# Linux audio helper

`titen-audio` lists PulseAudio-compatible sources and captures the selected
microphone and the default playback monitor. It runs one `parec` process per
source and writes TIT1 packets through one stdout writer.

## Build

```sh
go build -o build/linux/titen-audio ./native/linux
```

The helper is pure Go and builds with `CGO_ENABLED=0`. It needs `pactl` and
`parec` from `pulseaudio-utils` at runtime.

## Command contract

- No arguments capture both sources.
- `--microphone-only` captures the microphone.
- `--system-only` captures playback through the default monitor.
- `--microphone <source>` selects a source returned by `--list-devices`. A
  saved source that is missing or is a monitor source is rejected before
  capture starts.
- `--list-devices` prints a JSON array with `id` and `name` fields. Monitor
  sources are omitted.
- `--self-test` checks the frame format and source filtering without audio
  hardware.

The parent keeps stdin open while recording. A newline or EOF stops both
sources. `SIGINT` and `SIGTERM` also stop capture. Diagnostics use stderr.
When either `parec` process exits unexpectedly, both sources stop and the
helper returns a nonzero exit code.

## Audio contract

Packets use the same 24-byte little-endian TIT1 header as the macOS and
Windows helpers. Source 1 is the microphone and source 2 is playback. `parec`
converts to mono signed PCM16 at 48 kHz. The helper reads 20 ms blocks and the
Go pipeline resamples them to 16 kHz.

Both sources derive timestamps from one capture start and their own sample
counters, so microphone and playback frames share a clock. The helper does not
read server presentation timestamps; `parec` delivers audio in blocks, so
timestamps describe the position in the captured stream rather than the
instant a packet arrived.

The writer queue holds 32 frames. Overflow drops the oldest frame and emits a
warning. `--list-devices` includes hardware and virtual input sources but not
monitor sources, so the playback monitor stays out of the microphone picker.

## Verification limits

`--self-test` and the Go tests check framing, source filtering, and the
protocol shared with the desktop app. They do not prove live capture. Hardware
acceptance must verify microphone access, simultaneous playback loopback,
device changes, headphones, and clean shutdown.
