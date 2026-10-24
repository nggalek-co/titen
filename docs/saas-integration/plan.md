# Desktop SaaS integration

- [x] Read the Principles section of poteto-mode.
- [x] Frame.
- [x] Design the workflow.
- [x] Run the loop.
- [x] Keep the audit trail.
- [x] Verify and hand back.

## Done predicate

The desktop defaults to http://localhost:3000, saves a configurable server origin, pairs through the web device authorization flow, stores an origin-scoped token in the OS credential store, and sends audio through the managed transcription session. Local finalized transcripts survive. No provider credentials are requested. Tests exercise HTTP pairing and WebSocket audio/finalization. Run the real local worker integration without recording ambient audio.

## Design and throughput checkpoint

Compare extending the direct Soniox client with managed-mode branches against a separate SaaS client owning device/session contracts. Select the separate client to keep device tokens and Soniox keys in distinct protocols. Reuse the token assembler. The app owns recording lifecycle and local persistence; the web owns credit, provider keys, Google OAuth and delivery.

Independent work owns internal/saas, a read-only architecture audit, and the web stream completion fix respectively. The coordinator owns desktop UI, settings, service integration and verification. No two writers own a file. Existing dirty files and the moved PRD remain untouched except previously edited desktop documentation. Available models do not include the skill default families, so delegates inherit the current model.

## Units

1. Verify API contracts and model Endpoint, DeviceCode and Session.
2. Add managed client and executable HTTP/WebSocket tests.
3. Replace desktop credential forms and local Google sync with endpoint, pairing and web links.
4. Fix graceful server finalization required by the desktop.
5. Run native UI tester, race suite, Windows compilation and real localhost worker smoke.

## Gates

Provider credentials, actual microphone capture, real payments and deployment are outside this local integration proof. The web repo intent skill loader is unavailable because @tanstack/intent is not installed. No replacement is downloaded.
