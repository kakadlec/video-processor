## Context

`add-notification-webhook-delivery` built a delivery pipeline that is already channel-agnostic everywhere it matters. `cmd/notifier` consumes `video.jobs.terminal.events.v1`; `DeliverNotification` loads every deliverable preference for the event's user and type — `FindDeliverable` orders by channel and would return an e-mail row today — applies the enrolment boundary, then claims, attempts and resolves each one independently. `notification_deliveries` is keyed on `(user_id, event_type, channel, job_id)`, so two channels for one job are already two records with two claims and two outcomes. `domain.Deliverer` is one attempt at one delivery, and `DeliveryConfig` is the whole budget.

What is not channel-agnostic is the Notification context's own model of a preference, in three specific places written when `webhook` was the only channel:

```
  SetPreference ──▶ NewDestination(raw)          "absolute http/https URL with a host"
                └─▶ policy.CheckDestination(d)   SSRF judgement, unconditional
                └─▶ repository.Set(intent)       secret submitted? upsert : update
                                                  └─ zero rows ⇒ ErrSecretRequired

  FindDeliverable ─▶ NewSecret(value)            rejects ""
                  └▶ RestoreNotificationPreference(... secret ...)
                                                  └─ secret.IsZero() ⇒ ErrInvalidSecret

  schema.sql ─────▶ secret TEXT NOT NULL CHECK (secret <> '')
```

An e-mail preference fails at every one of them, and the last two matter more than the first: without them a row could be written and then never loaded, so a change that stopped at the CHECK would ship a preference that appears to register and delivers nothing — the exact failure the closed `Channel` set exists to prevent.

The address the e-mail is delivered to is the preference's own user-supplied `Destination`. `add-identity-user-registered-event` was removed from the backlog on 2026-09-07 for that reason; the address is therefore self-declared rather than the verified one Identity holds, and this design treats that as a stated exposure rather than an oversight.

## Goals / Non-Goals

**Goals:**

- Open the `Channel` set to `email` and make a preference on it storable, loadable, and deliverable end to end.
- Reuse the existing trigger, enrolment boundary, claim, fence, budget and recorded outcome without modification, so the second channel inherits every guarantee the first one established rather than restating them.
- Keep the three invariants that must become channel-conditional conditional in exactly one place each, and make the conditioning visible in the type system or the schema rather than in a comment.
- Introduce no new binary, no new consumer, no new queue, no new table, no new column, and no new budget term.
- Let a contributor observe a delivered message with `docker compose up --build` and nothing else.

**Non-Goals:**

- **Address verification.** No confirmation token, no round trip proving the registrant controls the address. It is a real gap and it is named in Risks; closing it is a change of its own.
- **HTML mail, attachments, templates, per-user senders, unsubscribe links, bounce handling, or a suppression list.** One plain-text message from one configured sender.
- **Any change to the webhook channel's behaviour.** Its destination rule, its policy, its signature, its secret requirement and its payload are untouched. The requirements that mention it are edited only where they were stated over destinations generally and are now stated over the channel whose destination is a connection target.
- **Any change to the budget's terms or their arithmetic.** `MaxClaimHold()` and `Validate()` must keep meaning what they mean.
- **Retiring `NewSecret`'s rejection of the empty string.** It stays; what changes is who calls it.

## Decisions

### 1. The channel routes to a `Deliverer` in the composition root, not inside the use case

`DeliverNotification` holds one `domain.Deliverer` and calls it once per preference. Rather than teaching the use case about channels, `cmd/notifier` composes a `Deliverer` whose `Deliver` dispatches on `preference.Channel()` to the webhook client or the SMTP client.

*Why:* the use case's job is the claim/attempt/resolve protocol, and that protocol does not vary by channel. Putting a `switch` inside it would put a transport concern in the application layer and give every future channel a reason to edit the one file that holds the fencing logic. Composing it outside means the use case, its tests and its disposition table are literally unchanged by this change — which is also what makes "the second channel inherits the first's guarantees" checkable rather than asserted.

*Alternative considered:* a `map[Channel]Deliverer` passed into `NewDeliverNotification`. Rejected: it is the same dispatch with a wider constructor signature and a new failure mode (a channel with no registered deliverer, discovered at delivery time rather than at startup). The composed deliverer can be built exhaustively over the closed `Channel` set at startup instead.

*Alternative considered:* a second consumer or a fourth binary for e-mail. Rejected: the event is the same event and the claim is per `(preference, job)` already. A second consumer would consume the same queue twice and each copy would have to ignore the other channel's preferences, which is more moving parts for no isolation the claim does not already provide.

### 2. `Destination` gains a per-channel constructor; the column does not change

`NewDestinationFor(channel Channel, raw string) (Destination, error)` replaces `NewDestination` at the four sites that build one — three reconstructions in `postgres/repository.go` (`Set`, `ListByUser`, `FindDeliverable`) and one in `SetPreference` — each of which already holds the channel. `webhook` delegates to the existing rule unchanged; `email` validates an address (decision 5).

*Why a constructor rather than a `Destination` interface or two types:* the value is one string, stored in one column, and every consumer either hands it to a transport or renders it. A sum type would push a type switch into three packages to buy nothing. Keeping `NewDestination` as the webhook rule's name and adding a channel-aware entry point also keeps the diff at the call sites honest — a reader sees which rule applies at each one.

*Why not one relaxed rule for both:* accepting `mailto:` in the webhook rule (or an address in a URL rule) would mean a webhook destination could be registered that no HTTP client can dial, and the write-time policy would have nothing to judge. The rules are genuinely different and the channel is genuinely known at every site.

### 3. The destination policy applies to the `webhook` branch, and this narrows its scope rather than relaxing its rule

`policy.CheckDestination` moves inside the webhook branch of `SetPreference`. `CheckAddr` in `net.Dialer.Control` is untouched and continues to guard every HTTP dial.

*Why this is not a hole:* `DestinationPolicy` judges **a connection target the user supplied**. The e-mail channel dials the relay named by this deployment's own configuration and never the address the user registered — that address is envelope data, not a dial target — so there is no user-supplied target for the policy to judge and no SSRF surface for it to guard. The policy's fail-closed property is preserved because the branch is taken on the closed `Channel` set: `ParseChannel` has already refused anything outside it, so there is no third branch that could silently skip both.

*Risk this creates and how it is bounded:* an operator who later adds a channel that *does* dial a user-supplied target must add it to the policy's branch. The spec states the rule as "a destination that is a connection target is judged by the policy at write and at dial", which is a rule about the kind of destination rather than a list of channels, so a future channel is covered by the requirement rather than by remembering this decision.

### 4. The secret invariant becomes conditional in three places, and all three are required

- **Schema:** `notification_preferences_secret_not_empty` becomes `CHECK (channel <> 'webhook' OR secret <> '')`.
- **Create path:** a third statement, alongside the existing upsert and update, that **names** `secret` and writes `''`. It must name it: the column is `NOT NULL` with deliberately no default, so a statement omitting it inserts NULL and violates the column, not the CHECK. Which statement runs is decided by the request exactly as it is today — a submitted secret picks the upsert, no secret on a `webhook` triple picks the update whose zero row count is `ErrSecretRequired`, no secret on an `email` triple picks the new one.
- **Aggregate:** `RestoreNotificationPreference` requires a non-zero secret only when the channel is `webhook`, and `FindDeliverable` parses the column through `NewSecret` only on that branch.

*Why the aggregate one is not optional:* `FindDeliverable` is the delivery path's only read and it restores every row it returns. Leaving the aggregate invariant unconditional would make an e-mail preference writable and permanently unreadable — and the failure would surface as a `FindDeliverable` error, which the disposition table classifies as a pre-attempt repository failure and therefore **requeues**, so one unloadable row would block the queue at prefetch 1 rather than failing visibly.

*Why `NewPreferenceIntent` is untouched:* it already declines to enforce the secret rule, deliberately, leaving it to the adapter's row count. That is precisely what lets the rule become channel-conditional in the adapter alone.

*Why `NewSecret` keeps rejecting `""`:* the empty string is not a secret, and every other caller is right to refuse it. What changes is that the e-mail branch does not call it.

### 5. Address validation is a contract, because header injection is the threat

`email` destinations are validated with `net/mail`'s `ParseAddress`, and then further constrained: the parsed address must equal the submitted string (no display name, no angle brackets, no comment syntax), the value must contain no CR, LF or NUL, and its length is bounded.

*Why stricter than `ParseAddress` alone:* an SMTP message is a header block, and a value containing CRLF that reaches a header injects headers — a second `To`, a `Bcc`, or a body. `ParseAddress` accepts `Name <a@b>` and quoted forms whose round trip is not byte-identical, which is a needless surface for a field that only ever has to be one address. Rejecting at registration makes it a `400` the owner can see, which is the same argument `notification-preferences` already makes about a NUL in a secret.

*Alternative considered:* sanitising at send time instead. Rejected for the reason the destination policy is applied at write time as well as dial time: a value that can never be delivered should fail where the user can read the error.

### 6. `net/smtp` from the standard library, dialed with a bounded dialer

No new module dependency. The connection is opened with a `net.Dialer` carrying the per-attempt deadline and handed to `smtp.NewClient`, rather than using `smtp.SendMail`, which offers no timeout at all.

*Why:* the feature set needed — EHLO, STARTTLS, PLAIN auth, one recipient, one body — is exactly what `net/smtp` covers, and every added module is a `govulncheck` surface the project gates every PR on. `net/smtp` is frozen to new features, which is a real limitation and is recorded in Risks; the `Deliverer` port is what makes replacing it later a change confined to one package.

### 7. One per-attempt timeout for every channel — no new budget terms

The SMTP attempt is bounded by `DeliveryConfig.Timeout`, the same term the webhook attempt uses, and no term is added.

*Why this is load-bearing rather than tidiness:* `MaxClaimHold()` is arithmetic over every term, `Validate()` refuses a `ReclaimBound` below twice it, and `cmd/notifier` treats a failure there as fatal. A channel with its own timeout would make the hold a maximum over channels, and `Validate` would have to reason about a combination no single delivery ever exhibits. One term keeps the computation reproducible and keeps the existing startup validation correct without being re-derived.

*Consequence accepted:* the default 5s bounds a whole SMTP conversation (dial, EHLO, STARTTLS, AUTH, MAIL, RCPT, DATA, QUIT), which is tighter than for one HTTP request. It stays tunable through the existing variable; see Risks.

### 8. Transport security is decided by whether credentials are configured

STARTTLS is used whenever the relay advertises it. When credentials **are** configured, STARTTLS is **required**: the adapter refuses to authenticate over a plaintext connection and fails the attempt as a policy refusal rather than sending the credential. When no credentials are configured, an unencrypted session is accepted.

*Why not govern this with `NOTIFICATION_ALLOW_INSECURE_DESTINATIONS`:* that switch relaxes the scheme rule and the address rule for **user-supplied destinations**, together and deliberately. The relay is operator-supplied and trusted; conflating the two would mean the switch that makes local development work also decides whether a production credential goes out in the clear. The credential-implies-TLS rule needs no switch: the local stack configures no credentials, so it works unchanged.

### 9. The message renders the same envelope data the webhook sends, and carries the delivery id in `Message-ID`

The body is `text/plain; charset=utf-8`, built from the same fields `webhook`'s envelope carries — event type, job id, occurred-at, and the outcome's own fields. It is not the JSON envelope pasted into a body, and it is not the Video Processing wire payload. The `Message-ID` header carries the `DeliveryID`, giving a receiver the same stable deduplication handle `X-FiapX-Delivery` gives a webhook receiver.

*Why plain text:* no HTML means no rendering surface and no injection question beyond the header rule in decision 5.

*Why the envelope's own version is not on the wire here:* the webhook carries `version` in the body because the body is a machine contract a receiver parses. A human-readable message has no parser to break, and putting a version in it would invite treating the prose as a contract. The e-mail's contract is the address it goes to and the fact that it names the job and the outcome.

### 10. `delivered` means the relay accepted the message, not that it arrived

For a webhook, a recorded `delivered` means the receiver answered 2xx. For e-mail it means the configured relay accepted the message for delivery. A later bounce is asynchronous, arrives at the envelope sender, and is invisible to this system.

*Why record it as `delivered` anyway:* it is the strongest statement this system can truthfully make, it is what the claim needs in order to resolve, and inventing a third status would imply a bounce-processing capability that is an explicit non-goal. The spec states the meaning so an operator reading the table is not misled.

### 11. The constraint migration is a guarded statement inside the existing advisory-locked `Migrate`

`internal/notification`'s `Migrate` already takes `pg_advisory_xact_lock` and runs its statements in one transaction. The constraint swap is added there, guarded on `pg_constraint` so it is a no-op once applied and safe to re-execute on every startup — the pattern `internal/video`'s schema already uses for statements that must reach a database whose table exists.

*Why no backfill and no rewrite:* the change only widens what the table accepts. Every existing row is a `webhook` row carrying a non-empty secret and satisfies the new constraint unchanged.

## Risks / Trade-offs

- **The address is self-declared and unverified — a user can register someone else's** → Bounded, not eliminated: a delivery is only ever triggered by a job that same user owns, so the volume is the registrant's own uploads rather than an open relay. Recorded in `docs/operations.md` and in the new capability. An address-verification flow is a named non-goal and a change of its own.
- **Header injection through the address** → Validation at registration rejects CR, LF, NUL, display names and any form whose round trip is not byte-identical (decision 5). The adapter interpolates no user-supplied string into a header that has not passed it.
- **A 5s per-attempt default may be tight for a real relay, where it is comfortable for one HTTP request** → The term is already tunable (`NOTIFICATION_WEBHOOK_TIMEOUT_SECONDS`) and raising it moves `MaxClaimHold()` and the `ReclaimBound` floor with it, by construction. A per-channel term was considered and rejected in decision 7.
- **The tunable's name says `WEBHOOK` but now governs both channels** → Kept rather than renamed: a rename is a breaking configuration change for a deployed stack, in exchange for a name. Documented in `docs/operations.md` as governing every channel. Recorded here so it reads as a decision rather than an oversight.
- **`net/smtp` is frozen to new features** → What the adapter needs is stable and covered. The `Deliverer` port confines a future replacement to one package, exactly as it would confine a change of HTTP client.
- **A bounce is invisible, so `delivered` overstates what is known** → Stated in the capability rather than left to inference (decision 10). An operator reading `notification_deliveries` is told what the status means.
- **A failure to reach the relay is a failure for every e-mail preference at once, unlike a webhook failure which is one endpoint** → It is still bounded by the same attempt budget and recorded per preference, so the blast radius is visible in the delivery table rather than silent. It does not affect webhook preferences for the same event: they are separate claims and separate attempts.
- **Widening the CHECK on a live table takes a lock** → It runs inside the advisory-locked transaction two replicas already serialise on, the table is small, and the statement is guarded so it executes at most once.

## Migration Plan

1. Deploy the schema change with the code, through the existing `Migrate` on `cmd/notification-api` and `cmd/notifier` startup. Either process may run it first; both take the same advisory lock, and the statement is idempotent.
2. No data migration. Existing rows are all `webhook` rows carrying secrets and satisfy the widened constraint unchanged.
3. Configure the relay for `cmd/notifier`. Until it is configured the process refuses to start, deliberately — a notifier that cannot send would store e-mail preferences and silently honour none, the failure mode the closed `Channel` set exists to prevent.
4. **Rollback** is the previous image plus re-tightening the constraint, and it is only safe while no `email` row exists — an `email` preference with an empty secret violates the original CHECK. If any exists, delete those rows before re-tightening. A rollback that skips the constraint is otherwise harmless: the old code refuses `email` at `ParseChannel`, so an `email` row is simply never read.

## Open Questions

None blocking. Two decisions above are the ones most worth re-reading at review: applying the destination policy to the `webhook` branch only (decision 3), and keeping one per-attempt timeout across channels (decision 7).
