# Direct Soniox streaming

The desktop streams PCM audio directly to Soniox with a temporary key. Titen's backend owns device authentication, credit reservations, transcript storage, and usage reconciliation. Temporary keys stay in memory and must not appear in logs or saved settings.

## Design choice

| Approach | Result |
| --- | --- |
| Proxy audio through Titen | Preserves existing metering, but keeps the backend in the audio path. |
| Temporary key with heartbeat-based billing | Rejected. A client heartbeat cannot establish provider usage. |
| Renewable grants within one meeting | Selected. Fresh single-use keys reconnect the provider while transcript sequence numbers and the billed meeting remain continuous. |
| One temporary key with a reserved allowance | Rejected for long meetings because a single-use key cannot reconnect after a provider disconnect. |

The authorization response carries the session identifiers, `wsUrl`, `temporaryApiKey`, `expiresAt`, `maxSessionDurationSeconds`, `model`, and the rate snapshot. The desktop validates the Soniox destination separately from its Titen endpoint. It sends only the temporary key to Soniox.

The backend reserves the allowance before issuing a single-use key. The key expires for new connections after 60 seconds. The current grant window is 10 minutes, and the backend permits a same-meeting renewal when the existing grant is within 90 seconds of its end. A network outage earlier in the grant can therefore pause transcription while the desktop keeps recording and spooling audio. Its server-bound `client_reference_id` is the session ID.

Heartbeats report liveness and upload finalized segments with stable sequence numbers. They do not establish billable usage. Ending a session marks it for reconciliation. Only provider usage records permit settlement and release of unused credit. Missing records retain the hold for retry or operator review.

## Limits

Soniox's session limit measures stream duration. It does not document a strict limit on the amount of audio a modified client can submit within that duration. A reserved duration is therefore not a guaranteed provider-cost ceiling. Use project spending controls and monitor usage before exposing this flow to untrusted clients.

Soniox documents usage logs for successfully completed requests. An absent record does not establish zero usage. A client crash, an unused credential, a delayed record, or a failed provider request can require reconciliation or operator review before reserved credit becomes available again.

## Work checklist

- [x] Inspect session authorization, heartbeat metering, transcript persistence, and provider documentation.
- [x] Compare the designs and select the direct authorization contract.
- [x] Write the throughput checkpoint.
- [x] Implement and verify the desktop transport and transcript synchronization.
- [x] Implement and verify backend authorization and reconciliation in a review copy. Applying it to `titen-web` remains blocked by workspace permissions.
- [ ] Verify the two applications together with a live provider session.
- [x] Prepare the backend patch for review. Commits and a PR are unavailable while repository metadata is read-only.

## Apply the backend change

The matching backend changes are in [titen-web-direct-soniox.patch](titen-web-direct-soniox.patch). The patch was checked against the sibling repository with `git apply --check`. It has not been applied there.

From the `titen-web` repository, apply the patch before rebuilding the desktop:

```sh
git apply --check ../titen/docs/saas-integration/titen-web-direct-soniox.patch
git apply ../titen/docs/saas-integration/titen-web-direct-soniox.patch
pnpm db:apply
```

Configure `SONIOX_API_KEY` with temporary-key, transcription, and usage-log access. Set `TRANSCRIPTION_RECONCILE_TOKEN` and arrange periodic authenticated calls to `POST /api/internal/transcription-usage` for abandoned sessions. Review the backend decision trail before deployment. Local `db:apply` does not migrate a remote D1 database.

## Verification and principle choices

Model the Domain shaped the explicit temporary-key authorization and transcript sequence fields. Separate Before Serializing Shared State gave the desktop and backend workers distinct write targets. Sequence Work into Verifiable Units separated contract review, implementation tests, and patch validation. Prove It Works required WebSocket protocol tests and database-backed billing tests.

Desktop direct-stream tests pass with the race detector over in-memory WebSocket connections. Application, audio, storage, platform, authentication, synchronization, and transcription package tests passed. All Go packages also compiled with `go test ./... -run '^$'`. Existing HTTP tests that bind TCP listeners cannot run in this sandbox. A live Soniox session and the combined applications remain unverified.

## Throughput checkpoint

- Blocking first steps. Agree on authorization, transcript upload, and settlement semantics before editing either client.
- Independent workstreams. The desktop owns `internal/saas`. The backend owns its routes, schema, and ledger integration. The coordinator owns application heartbeat cadence and this document.
- Shared mutable state. Workers have separate file scopes. Backend work uses `/tmp/titen-direct-backend` because this session cannot write to `../titen-web`.
- Smallest safe decomposition. One implementation owner per application, followed by cross-application contract review and behavioral checks.

## Provider references

- [Temporary API keys](https://soniox.com/docs/guides/temporary-api-keys).
- [Create temporary API key](https://soniox.com/docs/api-reference/auth/create_temporary_api_key).
- [Usage logs](https://soniox.com/docs/guides/usage-logs).
- [Get usage logs](https://soniox.com/docs/api-reference/other/get_usage_logs).
