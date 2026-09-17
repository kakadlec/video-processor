## Context

The page (`cmd/video-api/web/`) already signs a user in, keeps the access token and the account e-mail in `localStorage`, and calls every route on one origin through the gateway. The preference routes, the `email` channel, the SMTP delivery and the local Mailpit inbox all exist. What is missing is only the page's use of them. The brief asks to keep this simple.

## Goals / Non-Goals

**Goals:**
- A signed-in user can subscribe and unsubscribe to e-mail on job failure and on job completion from the page, and can see what they are subscribed to.
- The requirement "user is notified on error" can be demonstrated through the product: subscribe, upload a video that fails, see the message in Mailpit.

**Non-Goals:**
- A webhook form (URL, secret, `has_secret` display) — webhook stays API-only.
- Subscribing anyone by default, at registration or otherwise — `notification-preferences` forbids implicit preferences.
- Any Go, route, schema, compose or gateway change.
- A JavaScript test harness; the page has none today and this change does not introduce one.

## Decisions

**Checkboxes per event type, one shared address.** Two event types × one channel is two preferences. One address field keeps the form to three inputs; both writes send the same address. *Alternative:* an address per event type — more inputs, no demonstrated need.

**The address defaults to the account e-mail, but is the preference's own value.** On read, a stored `email` preference's `destination` fills the field (the one for `video_job.failed.v1` wins if both exist and differ); with none stored, the field falls back to the account e-mail kept at sign-in. The user may edit it. This matches the shipped rule that the delivered address is the preference's own `Destination`, not an identity lookup.

**Write only what the user selected, or what already exists.** Saving sends `PUT` for a checked box (`enabled: true`) and for an unchecked box whose preference was present on the last read (`enabled: false`, retaining the row as `notification-preferences` specifies). An unchecked box with nothing stored sends nothing. *Alternative:* always write both — simpler, but it would create disabled rows the user never asked for, which reads as an implicit preference.

**The secret field is never sent.** `email` needs no secret, and omitting it preserves any stored one.

**Errors map to existing page conventions.** `401` clears the session exactly as the other calls do; `429` shows a "try again shortly" message and does not retry (no polling here, so no backoff loop); `400` shows "endereço de e-mail inválido"; anything else a generic failure. All copy is pt-BR (language policy's `web/` exception).

**The enrolment boundary is stated on the page.** Delivery only considers preferences created before the event occurred, so the section carries a one-line hint that the subscription applies to videos processed from then on. Without it, subscribing after an upload already failed looks broken.

**Read timing.** The section loads its state when the page loads with a session and after a successful sign-in, alongside `loadFilesList`, and hides on sign-out.

## Risks / Trade-offs

- [The page's rate-limit budget is shared with uploads and polling] → at most one `GET` per sign-in/load and two `PUT`s per save; negligible against 60/min.
- [No automated test exercises the JavaScript behaviour] → the static-asset test asserts the section and the call are served; behaviour is verified manually against the running stack (subscribe → failing upload → Mailpit), as the frontend requirement's full-flow check already expects.
- [A disabled preference is still a stored row] → intended; re-enabling keeps its address.
