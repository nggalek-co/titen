# PRD 003: Tune the Titen desktop app

Status: Draft  
Owner: Titen desktop  
Date: 23 October 2026

## Problem

Titen must remain responsive while it captures audio, streams transcription, and shows a growing transcript. The current desktop uses MyGo's native GPU UI, a Go recording service, and a separate native audio helper. The app has automated correctness checks, but it has no repeatable measurements of startup, recording controls, transcript rendering, idle resource use, or long sessions. A framework rewrite has therefore no measured performance case.

This PRD covers measurement and targeted tuning of the current app. It does not assume that the UI renderer is the bottleneck. The work must identify whether a delay comes from UI work, audio capture, local storage, the provider, or the network before changing code.

## Outcome

Users can start and control recording without visible stalls, read a growing transcript smoothly, and leave Titen recording for hours without progressive resource growth or lost audio caused by UI work. Maintainers receive a repeatable way to measure those experiences and catch regressions.

## Scope

- macOS release build first. Add the same workload and metrics to Windows when a runnable Windows build is available.
- Cold launch, visible and hidden idle, start/pause/resume/stop, live partial and final transcript updates, transcript scrolling, opening a long saved transcript, and a four-hour recording with a 60-minute checkpoint. Include window close, app quit, unexpected exit, and network interruption during recording.
- UI frame time, input-to-feedback time, CPU, memory, allocations, and recording integrity. Measure provider and network latency separately from local display latency.
- Performance changes in `main.go`, MyGo view construction, update scheduling, transcript presentation, and related Go service boundaries only when a profile shows they contribute to a missed target. Session recovery changes follow the fault tests below.

Out of scope: SwiftUI or WinUI migration, redesigning the app, changing Soniox or billing behavior, and claiming live capture from a synthetic UI test.

## Session continuity and recovery

Treat window close, app quit, process loss, and network loss as different events. The current app hides the window on close and keeps recording. Quit calls Stop before exit. Finalized text survives a restart, but the temporary audio spool does not. A failed Soniox connection can end the stream. The behavior below is the target for this work, not a claim that recovery already works.

| Event | Required behavior |
| --- | --- |
| Close the window or open another app | Capture and transcription continue. The menu bar shows the same recording state and controls as the window. Reopening Titen returns to the same session and transcript without starting a second recording. |
| Choose Quit while recording | Show that Quit will end the recording. On confirmation, stop capture, drain queued audio, request provider finalization, save the resulting text and session status, then exit. Keep the window responsive and show finalization progress. If finalization fails or times out, save the session as incomplete and explain that recent speech may be missing. |
| Crash, forced quit, or power loss | On restart, recover the saved session and all finalized text. Mark the interrupted session incomplete, show its last confirmed transcript time and the unverified interval, and offer a new recording. Never present the interrupted session as complete or silently append a new recording to it. Recovering unfinalized audio across process loss requires a durable spool and an acknowledged replay protocol; do not promise it until both exist and pass fault tests. |
| Brief provider or network interruption | Show a reconnecting state in the window and menu bar while local capture continues within a bounded disk budget. Retry with backoff. Restore transcription without reordering or duplicating confirmed text or audio. If the provider cannot acknowledge which audio it received, mark the uncertain interval as a possible gap instead of claiming an exact transcript. |
| Prolonged outage, expired credentials, or full spool | Stop accepting audio before the storage limit is exceeded. Show an actionable degraded or incomplete state and the affected time interval. Preserve finalized text and any recoverable queued work. Never silently discard audio or report the session as complete. |

Track capture time, last locally saved final text, last provider-confirmed audio position when available, and any known or uncertain gap as separate fields. A network reconnect must not reset the session timer or create a second local session. Define a bounded retry and disk policy from measured capture rate and available space before implementation; report the chosen limits in the release report. If backend heartbeat or transcript upload fails while provider streaming continues, retain finalized text locally and retry upload without duplicate segments. Distinguish that condition from a Soniox audio-stream failure in the UI and logs.

The server grants one Soniox session for 600 seconds and admits renewal during its final 90 seconds. For a longer recording, the desktop must request a new grant with the same server `meetingId`, rotate the provider stream without starting another local meeting, keep transcript sequence numbers and timestamps ordered across grants, and update the heartbeat to the current session. The desktop must call `/end` only when the user ends the meeting: that endpoint marks the entire server meeting complete. A failed renewal must produce a visible incomplete or reconnecting state and identify any audio gap. The four-hour acceptance run must include multiple grant rotations and check that the server shows one meeting with ordered, nonduplicated segments.

## Measurement contract

Record the app commit, build flags, OS, hardware, display refresh rate, audio devices, input mode, account state, transcript size, and whether a provider is real or replayed. Use a release build and the same input data on both sides of a comparison. Keep compilation, login, permission prompts, and data generation outside timed runs.

The benchmark harness must report completed work and errors. A fast run that skipped audio, transcript updates, or persistence is invalid. For before-and-after comparisons, run at least five alternating trials of each build after warmup. Report the median and range, and treat differences within run-to-run variation as inconclusive. Keep raw traces and the exact reproduction command with the report.

| Experience | Measure | Initial acceptance budget |
| --- | --- | --- |
| Recording controls | Click or menu action to visible state feedback, measured while a session is active. Separate local feedback from server authorization and stop finalization. | p95 at most 150 ms for local feedback. No duplicate window or lost action. |
| Live transcript | Receipt of a partial or final update in the desktop process to its first visible frame. | p95 at most 500 ms under a sustained, representative stream. Text remains ordered and complete. |
| Scrolling and updates | MyGo frame build, layout, paint, and present time at 60 Hz with a long transcript. Count frames that miss the display interval. | p95 at most 16.7 ms and p99 at most 33 ms. Confirm on the target display rather than applying these limits to a different refresh rate. |
| Visible and hidden idle | Process CPU, UI frame count, and resident memory after warmup. | No continuous redraw while nothing visible changes. Hidden recording must not spend CPU on UI frames. Record a baseline before setting a hardware-specific CPU budget. |
| Long session | CPU, Go heap, resident memory, spool size, dropped audio, and finalized transcript continuity at 60-minute intervals across four hours. Include one window-hidden run and a separate sleep/wake recovery check. | No sustained growth after bounded buffers and the live transcript window reach steady state. No UI-induced audio drops or missing finalized text. Report any capture gap after sleep rather than silently treating it as recorded audio. |
| Startup and saved history | Launch to usable window; open and scroll a long saved transcript. | Record a baseline and preserve or improve it. Set a time budget from a real user run before accepting a startup-specific change. |

The numeric UI budgets are product targets, not measured current results. If the baseline already meets them, preserve that result and focus on the remaining failures. If a budget is unattainable on the reference device, record the limiting evidence and revise the budget before using it as a release gate.

## Work plan

1. **Build the replay and capture harness.** Replay a fixed stream of partial and final transcript updates, including long speaker turns, and a fixed audio workload. Count delivered updates, persisted segments, dropped frames, and errors. Keep a separate real microphone and system-audio smoke run for permissions and device behavior.
2. **Capture the baseline.** Measure the workload matrix in a packaged release build. Use `MYGO_FRAME_STATS` for slow UI frames, Go CPU and heap profiles for the desktop process, and macOS Instruments for process and UI-thread activity. Attribute wait time to local work or external services.
3. **Rank bottlenecks.** Record each candidate's measured share of the user-visible delay. Inspect the 250 ms snapshot update loop, transcript row regrouping, long text layout, history loading, and menu updates only if the traces point there. Do not promote a code-reading guess to a finding.
4. **Tune one bottleneck at a time.** Make the smallest change that removes a measured cost. Run the same workload and correctness checks after each change. Revert changes that do not improve the user-visible path beyond measurement noise.
5. **Verify the release build.** Compare alternating before-and-after runs on the same device, run the Go race suite and UI tests, check recording and transcript integrity, and perform the real audio smoke test. Run the four-hour endurance check with the window hidden, a one-hour visible check, and a separate sleep/wake recovery check. Inject window close, Quit, process kill, brief and prolonged network loss, and reconnect flapping during active capture. Check saved text, audio gaps, state shown in both controls, disk use, and restart behavior.

## Release gates

- A baseline report identifies the limiting code path or external wait for every experience that misses its budget. It includes traces, run counts, error counts, and the exact builds used.
- Each accepted tuning change improves the measured user-visible path beyond trial variation or fixes a budget miss. It does not merely improve an isolated microbenchmark.
- Start, pause, resume, stop, and input selection retain their existing behavior. Menu bar state, transcript order, persistence, and provider finalization meet the continuity requirements above.
- The four-hour run shows bounded memory and spool use, no UI-induced audio loss, and no progressively worsening response time. The visible and sleep/wake checks have explicit results.
- Closing the window leaves one recording active. Quit either finalizes it or leaves an explicitly incomplete session. After a forced exit, Titen restores finalized text and identifies the interrupted interval.
- Brief and prolonged network-fault runs report captured, sent, confirmed, and missing audio intervals. They show bounded disk use, no duplicate finalized segments, and the same reconnecting or incomplete status in the menu bar and window. Any unverified interval is visible to the user.
- The packaged app passes signature and archive checks. The report separates synthetic replay results from real hardware and provider results.

## Decision after tuning

Keep MyGo if the budgets pass or if the remaining delay lies outside UI rendering. Consider a one-screen SwiftUI or WinUI prototype only if a repeatable profile shows that the UI framework itself remains the limiting factor after targeted tuning. Compare that prototype with the same data, device class, release configuration, and user-visible metrics before proposing a migration.

## References

- [Titen architecture](../architecture.md) and [manual capture checks](../manual-testing.md).
- [MyGo rendering and frame statistics](https://github.com/egoist/mygo/blob/main/docs/ui/rendering.md) and [MyGo benchmark scope](https://github.com/egoist/mygo/blob/main/docs/ui/performance.md).
