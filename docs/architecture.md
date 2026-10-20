# Titen architecture

Titen records microphone and meeting playback on macOS, transcribes a mixed stream, and saves finalized text locally before Google Docs synchronization. The native window is a client of the Go application service. Closing or minimizing that window does not end recording.

## Validated dependencies

Research was performed on 20 October 2026 against the pinned MyGo v0.5.5 source, the installed macOS SDK, and official provider documentation.

| Requirement | Evidence and decision |
| --- | --- |
| Native GPU UI | MyGo v0.5.5 ui/doc.go and examples/counter-native demonstrate Content with ui.View. TextInput, TextArea, Select, Checkbox, List, Meter, buttons and scrolling exist in ui/api_gen.go. The starter URL-based WebView is replaced. |
| Application lifecycle and tray | MyGo examples/menubar-native and app.go provide NewTray, menus, preventable close and quit events, and OnWindowAllClosed. GlobalShortcut.Register exists, but the PRD does not require a global shortcut. |
| Independent microphone and playback | Apple SCStreamConfiguration.captureMicrophone and microphoneCaptureDeviceID, with SCStreamOutputType.microphone and .audio, expose separate streams on macOS 15 and later. |
| Swift helper | Swift is not a MyGo requirement. A separate process contains native capture ownership and failures. Go retains session, transcription and storage logic. |
| Provider format | Soniox accepts pcm_s16le with sample_rate and num_channels. Titen sends 16 kHz mono little-endian signed PCM16 as binary WebSocket messages. |
| Provider authentication and lifetime | Current Soniox documentation specifies Authorization on the connection, stt-rt-v5, keepalive, and a 300 minute stream limit. Streams rotate before the limit. |
| Google authorization | Desktop OAuth uses a system browser, loopback redirect, state and S256 PKCE. The drive.file scope authorizes app-created or explicitly app-authorized documents and is accepted by Docs create and batchUpdate. |
| Existing documents | A pasted ID does not grant access. With drive.file, an existing document must already be authorized for this app. Arbitrary Drive browsing would require a separate picker or broader scope and is not implemented. |
| Google retry safety | A batch inserts transcript text and deterministic named ranges atomically using requiredRevisionId. A retry reads the ranges and skips committed operations after a lost response. Document creation has no equivalent client idempotency key; an ambiguous create response can leave an orphan document before a retry creates the attached one. |

## Alternatives considered

| Approach | Trade-off | Selection |
| --- | --- | --- |
| Native MyGo with external Swift and Unix socket | Bidirectional framing, but socket discovery and cleanup add lifecycle work. | Viable alternative. |
| Native MyGo with external Swift and binary stdout | Private inherited pipe, bounded framing, process-owned transport and no socket path. Device selection uses process arguments. | Selected. |
| Native MyGo with in-process Swift bridge | Avoids IPC but couples ABI, native failures and memory ownership to Go. | Rejected. |
| MyGo WebView with Swift helper | Matches the original starter but fails the native GPU UI requirement. | Rejected. |
| Microphone library with virtual loopback driver | Adds installation and device routing requirements. | Rejected. |

Two application structures were compared independently. A service with snapshots uses direct lifecycle methods and isolated workers. An actor bus needs command types, reducers and mailboxes for the same single active meeting. The service design was selected because one recording lifecycle is the actual invariant. Sync retains its own durable queue and does not become a recording state.

## Data ownership

- internal/app owns the recording state machine, session cancellation, pause and resume, and immutable UI snapshots.
- internal/audio owns binary frames, helper lifecycle, timestamp alignment, PCM conversion, bounded queues, and mixed chunks.
- native/macos owns ScreenCaptureKit, permissions, device discovery, AVAudioConverter, and native sample delivery.
- internal/transcription owns the Soniox connection, partial updates, finalized tokens, stream rotation, and reconnect behavior.
- internal/transcript owns finalized segment identity and speaker/timestamp metadata.
- internal/storage owns private SQLite meetings, finalized segments, and transactional outbox entries.
- internal/auth owns browser OAuth, token refresh and OS credential storage.
- internal/sync owns four second outbox polling, retry scheduling, and Google document updates.
- main.go owns native views and menu bar actions. UI drafts belong to the UI thread. Slow work runs outside rendering.

The recording states are Ready, Starting, Recording, Paused and Finishing. Pause terminates native capture while leaving the meeting and transcription session open. Resume launches capture with the saved microphone choice. Stop closes capture, drains native sample and writer queues, signals Soniox end-of-stream, and allows up to 15 seconds for final transcription before marking the meeting complete. A longer outage can leave unfinalized audio missing. Native stream interruptions retry capture up to three times within the same meeting; permission failure ends the meeting with an actionable error. A paused meeting can apply a different microphone or source mode before resuming.

## Audio transport and processing

The helper emits source-tagged frames with timestamps, rate, channel count, sample format, payload length and raw audio. Go validates header fields and payload bounds before allocation. No PCM enters JSON or base64. System and microphone timestamps share the native monotonic clock. The live view retains the latest 500 finalized segments; complete history remains in SQLite. The mixer allows a short reorder window, converts channels and rate, contributes silence for missing sources, and applies half gain to each input before summing.

Headphones do not change the system playback capture path. They reduce microphone pickup of playback. Mixed mode does not provide acoustic echo cancellation. Separate provider sessions remain a future mode because frames retain source identity before mixing.

Queues are bounded. Overload can discard frames and must be treated as incomplete audio. Provider reconnection does not replay unconfirmed audio, which avoids replay duplicates but may lose speech during an outage. A private, unlinked disk spool preserves unsent PCM during a running session, with a 1 GiB total meeting limit. It retains audio until session cleanup and cannot recover audio after process exit. Finalized text, pending document creation and pending cloud operations survive restarts.

## Permissions, packaging, and security

The app targets macOS 15 and later. The app bundle declares microphone and screen capture usage descriptions. macOS requires microphone and screen/system-audio recording approval for simultaneous capture. Microphone-only mode, including the Settings input test, uses AVCaptureSession and needs only microphone approval. Simultaneous and system-audio capture use ScreenCaptureKit. Both paths feed one conversion and framing function. The Settings test reports local RMS levels for ten seconds or until stopped, without persistence or cloud clients. Denial and capture failure travel back to the visible status. ScreenCaptureKit still uses a content filter even though Titen consumes audio only.

The local development bundle is not sandboxed. A sandboxed distribution would additionally need audio-input and network entitlements and separate validation. The build script signs the helper and app ad hoc for local testing. Developer ID signing, hardened runtime, notarization and distribution are release work.

Soniox credentials and Google tokens use Keychain. SQLite stores transcript text and document references, never provider keys or refresh tokens. The configuration directory is private to the user and database files have mode 0600. Logs exclude audio, tokens and transcript content.

The four second sync interval normally requires at most 15 batch writes per minute per active document. Google documents a default user quota of 60 writes and 300 reads per minute per project. Multiple pending documents share that quota. HTTP 429 and other failures retain operations and use capped exponential backoff.

Runtime lifecycle, provider connection retries and sync attempts emit structured slog events. They exclude credentials, raw audio and transcript text.

## Official references

- [MyGo native UI](https://mygo.egoist.dev/docs/ui).
- [MyGo application lifecycle](https://github.com/egoist/mygo/blob/main/docs/app.md).
- [ScreenCaptureKit microphone capture](https://developer.apple.com/documentation/screencapturekit/scstreamconfiguration/capturemicrophone).
- [ScreenCaptureKit microphone device selection](https://developer.apple.com/documentation/screencapturekit/scstreamconfiguration/microphonecapturedeviceid).
- [ScreenCaptureKit microphone output](https://developer.apple.com/documentation/screencapturekit/scstreamoutputtype/microphone).
- [Soniox WebSocket API](https://soniox.com/docs/api-reference/stt/websocket-api).
- [Google desktop OAuth](https://developers.google.com/identity/protocols/oauth2/native-app).
- [Google Docs batch updates and write control](https://developers.google.com/workspace/docs/api/reference/rest/v1/documents/batchUpdate).
- [Google Docs authorization](https://developers.google.com/workspace/docs/api/auth).
- [Google Docs quotas](https://developers.google.com/workspace/docs/api/limits).

Live permission, hardware and provider acceptance results are recorded separately in verification.md.
