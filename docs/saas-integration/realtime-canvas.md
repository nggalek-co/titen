# Realtime transcript canvas

Viewers watch a meeting transcript live on a `/realtime` page in titen-web instead of refreshing a Google Doc. The Google Doc remains the durable artifact written asynchronously after segments persist.

## Design choice

| Approach | Result |
| --- | --- |
| Persist-first render. Segments land in `transcript_segments`, the canvas reads persisted rows. | Selected. One writer, one `seq` ordering contract, and viewers see exactly what the durable store holds. |
| Double-send render lane. Desktop pushes to a render channel in parallel with persistence. | Rejected. Two output streams diverge when an upload fails mid-meeting; the canvas would show text the final document never contains. |
| Ephemeral partial relay alongside finals. | Deferred. Partials only improve in-utterance liveness; add them as a non-durable field on the same ingest if stall complaints appear. |
| Proxy audio through titen-web. | Rejected, unchanged. Workers `fetch` rejects WebSocket upstreams; desktop keeps direct Soniox with temporary keys. |

## Why the canvas beats faster Docs sync

The Google Docs API caps writes at 600 per minute per project and 60 per minute per user per project. Reads cap at 3000 per project. Near-realtime per-segment writes from 100 concurrent recorders produce roughly 2000-4000 writes per minute, five or more times over the project limit. Batching pending segments into one `batchUpdate` per document helps but still exceeds the project quota above roughly 20-30 concurrently speaking users. The quota is shared across every user of the project, so one meeting cannot starve another only by pacing.

Rendering from `transcript_segments` removes the live-viewer path from Google entirely. `doc_jobs` then runs at a relaxed cadence, tens of writes per minute total across all meetings, far under the limit. The named-range marker plus `requiredRevisionId` reconciliation protocol stays unchanged; it only loses its realtime obligation.

## Constraints kept

- Finals reach titen-web through the existing heartbeat upload with stable per-session `seq` numbers, capped at 100 per request. No new ingest contract.
- Ordered `seq` persisted rows are the single ordering contract between desktop, storage, canvas, and doc jobs.
- Doc jobs keep at-least-once semantics reconciled by markers in the document, not by job bookkeeping alone.
- Partials remain undelivered. They carry no durable state and add a second stream shape only when a measured need exists.

## Limits

- Canvas latency is bounded by heartbeat flush cadence plus a read hop, around one to five seconds behind speech. Soniox endpoint detection can hold a long utterance unfinalized for longer; the canvas stalls during that window and nothing short of partials fixes it.
- Soniox concurrent stream limits are plan-dependent and unverified for 100 simultaneous recorders. The account limit must be confirmed before launch at that scale.
- The Google Doc's role is open: live artifact on a slow cadence or end-of-meeting export. The doc-jobs runner supports either; the choice is product-facing, not architectural.
- `internal/sync` remains in the desktop as an unused package. It is the legacy direct-to-Google path and can be deleted once the canvas ships.

## References

- Desktop upload: `internal/saas/stream.go` (`syncSegments`, five-second flush ticker).
- Server ingest: `src/routes/api/heartbeat.ts` and `persistSegment` in `src/lib/transcription/service.ts` (titen-web patch `titen-web-direct-soniox.patch`).
- Doc delivery: `src/lib/google/sync.ts` and `src/lib/google/docs.ts` in titen-web.
- Google quota figures: [Docs API usage limits](https://developers.google.com/workspace/docs/api/limits).
