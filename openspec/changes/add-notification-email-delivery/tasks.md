## 1. Open the channel set and make an e-mail destination expressible

- [ ] 1.1 Add `ChannelEmail = "email"` to `internal/notification/domain/channel.go` and accept it in `ParseChannel`; replace the constant's comment, which currently states why there is deliberately no e-mail value. Update `channel_test.go` so the accepted set is asserted as exactly two values and an arbitrary third is still refused.
- [ ] 1.2 Add e-mail address validation to `internal/notification/domain`: a single addr-spec address, rejecting a display name, angle brackets, comment syntax, any value whose canonical rendering is not byte-identical to the input, any CR/LF/NUL, and anything over the length bound. Table-driven tests covering each rejection, including the header-injection cases, are the point of this task.
- [ ] 1.3 Add `NewDestinationFor(channel Channel, raw string) (Destination, error)` to `internal/notification/domain/destination.go`, delegating to the existing URL rule for `webhook` and to 1.2 for `email`. Leave `NewDestination` in place as the webhook rule. Test both branches, including that a URL is refused for `email` and an address for `webhook`.
- [ ] 1.4 Make `RestoreNotificationPreference` require a non-zero `Secret` only when the channel signs, and update the type's doc comment, which currently states the invariant unconditionally. Test that an `email` preference restores with a zero secret and that a `webhook` one still does not.

## 2. Storage: the conditional secret rule

- [ ] 2.1 Replace the `notification_preferences_secret_not_empty` constraint in `internal/notification/infrastructure/postgres/schema.sql` with a channel-conditional one, as a statement guarded on `pg_constraint` so it is a no-op once applied and safe to re-execute on every startup. Keep the column `NOT NULL` with no default, and keep the comment explaining why there is no default.
- [ ] 2.2 Add the third statement to `repository.go`: an upsert that **names** `secret` and writes the empty string, used when no secret was submitted and the channel does not sign. Route `Set` to one of the three cases; leave `ErrSecretRequired` reachable exactly as before for a `webhook` create with no secret.
- [ ] 2.3 Make the three `Destination` reconstruction sites in `repository.go` (`Set`, `ListByUser`, `FindDeliverable`) use `NewDestinationFor` with the row's own channel, and make `FindDeliverable` parse the secret through `NewSecret` only on the signing branch.
- [ ] 2.4 Extend `repository_test.go` and `migrate_test.go` against a real database: an `email` create with no secret stores a row and reads back with `has_secret` false; a `webhook` create with no secret is still `ErrSecretRequired`; a stored `email` row round-trips through `FindDeliverable`; the migration is idempotent across two concurrent runs and rewrites no existing row.
- [ ] 2.5 Confirm `TestNoQueryOutsideFindDeliverableSelectsTheSecret` still passes and that the new statement does not project the secret column.

## 3. Application: let a preference on either channel be written

- [ ] 3.1 Branch `SetPreference.Execute` on the parsed channel: build the destination through `NewDestinationFor`, and apply `policy.CheckDestination` on the `webhook` branch only. Take the branch on the closed channel set so there is no path that applies neither rule.
- [ ] 3.2 Extend `set_preference_test.go`: an `email` preference with an internal-looking domain is stored; a `webhook` preference the policy refuses is still `ErrDestinationRefused`; an `email` destination that is a URL is refused; a `webhook` destination that is an address is refused.
- [ ] 3.3 Confirm `DeliverNotification` and its tests are untouched by this change, and that `NewPreferenceIntent` is untouched.

## 4. The SMTP adapter

- [ ] 4.1 Create `internal/notification/infrastructure/smtp` with configuration loaded from the environment: relay address, envelope sender, optional credentials. Validate at load time and report a missing required variable by name.
- [ ] 4.2 Implement the message: `text/plain; charset=utf-8`, built from the same outcome fields the webhook envelope carries, with the delivery identifier in a header a receiver can deduplicate on and the sender from configuration. No signature, no HTML, no attachment, no remote reference, no credential, and no recipient address in the body.
- [ ] 4.3 Implement `Deliver` against `domain.Deliverer`: dial with a `net.Dialer` bounded by the injected per-attempt timeout and hand the connection to `smtp.NewClient` rather than using `smtp.SendMail`, which cannot be bounded. Use STARTTLS when advertised; when credentials are configured, require an encrypted session and fail the attempt rather than authenticate over plaintext.
- [ ] 4.4 Report every failure as the existing `*domain.DeliveryError`, built from our own classification. Assert in tests that neither the recorded reason nor any log line contains the recipient address or the relay's own error text.
- [ ] 4.5 Assert the adapter never reads a preference's `Secret` — a source-level test in the spirit of `TestNoQueryOutsideFindDeliverableSelectsTheSecret`, since a behavioural test cannot show that a value was not read.
- [ ] 4.6 Tests drive a scripted in-process SMTP listener over a local pipe or loopback, covering: accepted message, refused recipient, timeout, and credentials offered to a relay with no encryption.

## 5. Compose the second channel in `cmd/notifier`

- [ ] 5.1 Add the routing `Deliverer` to `cmd/notifier`: dispatch on `preference.Channel()` to the webhook client or the SMTP client, built exhaustively over the closed channel set at startup so a channel with no implementation fails there rather than at delivery time.
- [ ] 5.2 Load the SMTP configuration in `setupNotifier`, before any I/O, alongside the existing configuration and the delivery-budget validation, and make a missing variable fatal with the variable named.
- [ ] 5.3 Confirm no budget term is added and that `DeliveryConfig.MaxClaimHold()`, `Validate()` and the shutdown drain are unchanged; add a test asserting the SMTP attempt is bounded by the same injected timeout the webhook attempt uses.
- [ ] 5.4 Extend `cmd/notifier`'s tests for the new failure paths in `setupNotifier` and for the routing deliverer's dispatch, including that an unknown channel cannot be constructed.

## 6. Local stack

- [ ] 6.1 Add a mail service to `docker-compose.yml` from an image this repository does not build, with its own UI reachable for inspection, and point `notifier` at it with no credentials so the plaintext path is the one exercised locally.
- [ ] 6.2 Confirm `app-test` still runs the full suite, and that no other service gains configuration it does not use.

## 7. Verify

- [ ] 7.1 `go vet ./...` and `go test ./... -v` pass locally, run through `docker compose run --build --rm app-test go test ./... -v` if `ffmpeg`, MinIO or the databases are not available on the host.
- [ ] 7.2 `gosec ./...` and `govulncheck ./...` are clean — the SMTP adapter is new outbound network code and is what these two are for here.
- [ ] 7.3 Exercise the path end to end on the compose stack: register an `email` preference with no secret, upload a video, and find the message in the mail service; then register a `webhook` preference for the same event type and confirm one job produces two independent delivery records.

## 8. Finalization (after the implementation PR merges — not part of it)

- [ ] 8.1 Update `docs/architecture.md`, `docs/domain-model.md`, `docs/flows.md`, `docs/operations.md` and `docs/development.md` for the second channel, the per-channel destination rule, the narrowed secret invariant, the new variables, the self-declared-address consideration, and what a recorded `delivered` means on this channel.
- [ ] 8.2 Update `CLAUDE.md`: the channel set is no longer `webhook` alone, the secret invariant is channel-conditional in three places, and the destination policy applies to the webhook branch only.
- [ ] 8.3 Flip the `add-notification-email-delivery` row in `docs/roadmap.md` to archived with links, and update the Phase 7 summary — this change closes the phase.
- [ ] 8.4 `npx --yes @fission-ai/openspec validate add-notification-email-delivery --strict --no-interactive`, then archive.
