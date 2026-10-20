# Validate macOS meeting capture

Run these checks with a locally built Titen.app and an account authorized to use Soniox and Google Docs. Do not infer success from an active process or a passing unit test. Record the date, macOS version, audio devices, app revision, observed results, and any visible error for each case.

| Case | Procedure and required observation | Result in this environment |
| --- | --- | --- |
| Settings microphone test | Select an input and click Test microphone for 10 seconds. Speak and watch the test meter. Verify early stop, automatic stop, and no new meeting or cloud document. | Local helper and UI tests passed. Real helper returned microphone permission denial. Live signal remains unverified. |
| Microphone only | Choose Microphone only, record a spoken sentence, and stop. Only the microphone meter should respond. Verify the final words in History and the Doc. | Not run. |
| System audio only | Choose System audio only, play spoken audio, and stop. Only the system meter should respond. Verify the words in History and the Doc. | Not run. |
| Both inputs | Choose Both. Alternate speaking and meeting playback. Both meters must respond independently and both utterances must appear. | Not run. |
| Headphones | Repeat simultaneous capture with wired and Bluetooth headphones. Confirm remote meeting playback remains captured. | Not run. |
| Google Meet | Join a test call, alternate local and remote speech, then verify both sources and document text. | Not run. |
| Zoom | Repeat a test call and verify local and remote speech. | Not run. |
| Teams | Repeat a test call and verify local and remote speech. | Not run. |
| Indonesian and English | Speak a distinct phrase in each language with id,en hints. Verify partial updates, final words, timestamps and diarization. | Not run. |
| Pause and resume | Speak, pause, speak again, resume, and speak once more. Paused speech must be absent. The meeting and document must stay the same. | Automated lifecycle test passed. Live test not run. |
| Device switching | Pause, choose another microphone, save, and resume. Confirm the selected input now drives the microphone meter. Unplug an active input and inspect recovery or the actionable failure. | Automated configuration test passed. Hardware test not run. |
| Permission denial | Deny microphone and screen capture in separate runs. Confirm the app stops truthfully and explains recovery. Restore permission and record again. | Fake-helper failure test passed. OS prompts not tested. |
| Minimize and hide | Record speech while minimized and after closing the window. Reopen from the menu bar and verify the intervening finalized speech. | Not run. |
| Network loss | Disconnect the network during speech. Restore it while recording. Verify reconnection, ordered unsent-audio recovery, and explicit warnings about any unconfirmed speech. | Mock protocol tests passed. Live test not run. |
| Google outage and restart | Block Google access while Soniox remains reachable. Finalize speech, quit, relaunch, restore access, and verify one copy of every finalized batch. | SQLite restart and HTTP ambiguity tests passed. Live test not run. |
| Document creation outage | Start a new auto-sync meeting while Google is unavailable. Relaunch, restore Google, and verify document creation and pending text retry. | Automated durable creation test passed. Live test not run. |
| Native interruption | Interrupt the capture stream. Confirm at most three restarts, the same meeting identity, and a truthful terminal error if recovery fails. | Fake-helper recovery test passed. Native test not run. |
| Stop and quit | Stop while speaking, then repeat with Quit. Confirm final provider text is drained or a timeout explicitly reports missing unfinalized speech. | Fake-helper lifecycle and WebSocket flush tests passed. Native tail test not run. |
| Sixty minute session | Record microphone and playback for at least 60 minutes. Check timestamps, source levels, partial responsiveness, finalized continuity, memory, spool growth, helper cleanup and Google text. | Not run. |

The synthetic native recording screenshot in screenshots/native-recording.png is a UI test fixture. It contains invented text used only to inspect layout, not a captured meeting.
