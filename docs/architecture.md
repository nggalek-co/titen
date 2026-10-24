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

The recording states are Ready, Starting, Recording, Paused and Finishing. Pause terminates native capture while leaving the meeting and transcription session open. Resume launches capture with the saved microphone choice. Stop closes capture, drains native sample and writer queues, signals Soniox end-of-stream, and allows up to 15 seconds for final transcription before marking the meeting complete. A longer outage can leave unfinalized audio missing. Native capture interruptions retry with increasing delays until capture restarts or the user stops; permission failure ends the meeting with an actionable error. A paused meeting can apply a different microphone or source mode before resuming.

## Audio transport and processing

The helper emits source-tagged frames with timestamps, rate, channel count, sample format, payload length and raw audio. Go validates header fields and payload bounds before allocation. No PCM enters JSON or base64. System and microphone timestamps share the native monotonic clock. The live view retains the latest 500 finalized segments; complete history remains in SQLite. The mixer allows a short reorder window, converts channels and rate, contributes silence for missing sources, and applies half gain to each input before summing.

Headphones do not change the system playback capture path. They reduce microphone pickup of playback. Mixed mode does not provide acoustic echo cancellation. Separate provider sessions remain a future mode because frames retain source identity before mixing.

Queues are bounded. Overload can discard frames and must be treated as incomplete audio. During a provider outage, a private, unlinked disk spool preserves unsent PCM up to 1 GiB, and the client reconnects to the same meeting with a renewed grant. The backend's 409 renewal guard can delay reconnection until the current 10-minute grant is within its 90-second renewal lead. WebSocket audio writes have no delivery acknowledgement: the failed write is retried, but a write accepted by the local socket immediately before disconnect may be missing or duplicated. The spool is deleted at session cleanup and cannot recover audio after process exit. Finalized segments are saved to SQLite as they arrive; startup recovers unfinished meetings as interrupted.

Unexpected audio-helper exits retry with increasing delays until capture restarts or the user stops the meeting. This supports recovery after wake, but no audio can be captured while the computer sleeps; the meeting is marked incomplete to preserve that fact. Closing the window hides the app and leaves the service running.

## Permissions, packaging, and security

The app targets macOS 15 and later. The app bundle declares microphone and screen capture usage descriptions. macOS requires microphone and screen/system-audio recording approval for simultaneous capture. Microphone-only mode, including the Settings input test, uses AVCaptureSession and needs only microphone approval. Simultaneous and system-audio capture use ScreenCaptureKit. Both paths feed one conversion and framing function. The Settings test reports local RMS levels for ten seconds or until stopped, without persistence or cloud clients. Denial and capture failure travel back to the visible status. ScreenCaptureKit still uses a content filter even though Titen consumes audio only.

The app is not sandboxed, so `packaging/macos/Titen.entitlements` is an explicit empty plist. The build script signs the app and helper with the persistent self-signed `Titen Self-Signed Code Signing` identity. Their bundle IDs and designated requirements are held in `scripts/signing-config.sh`; the shared requirement binds the stable ID to the certificate common name. Version-tag builds import the same PKCS#12 identity from GitHub Secrets. No paid Apple Developer account is needed, but self-signing does not notarize the app or remove Gatekeeper's first-launch warning. A future sandboxed build would need different entitlements and separate permission validation.

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
