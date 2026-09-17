## Why

The hackathon brief (`docs/project-requirements.pdf`) requires that "em caso de erro, um usuário pode ser notificado (e-mail ou outro meio de comunicação)". The back end already does this end to end — `PUT /api/notification-preferences` stores an `email` preference and `cmd/notifier` delivers through SMTP — but the only way to subscribe is a hand-written `curl`. A user of the web page has no way to ask to be notified, so the requirement cannot be shown working through the product, and it cannot be shown in the presentation video without leaving the product.

## What Changes

- **A "Notificações por e-mail" section on the web page**, shown only while signed in: one address field (pre-filled with the signed-in account's e-mail) and two checkboxes, "quando o processamento falhar" and "quando o processamento for concluído", plus a save button.
- **On sign-in (and on page load with a session) the section reads `GET /api/notification-preferences`** and reflects the caller's stored `email` preferences: a checkbox is checked when its preference exists and is enabled, and the address field shows a stored address when one exists.
- **Saving writes through the existing `PUT /api/notification-preferences`**, one request per event type, with channel `email` and no secret. A request is sent for a checked box, or for an unchecked box whose preference already exists (writing it disabled). An unchecked box with no stored preference sends nothing, so saving never creates a preference the user did not ask for.
- **Webhook stays API-only.** The page offers no webhook form; nothing about the webhook channel changes.
- No Go code, no route, no API contract, no schema, and no compose change. The gateway already routes `/api/notification-preferences` to `notification-api` on the page's own origin, and the shared CORS policy already allows `PUT` for exactly this write.

Not breaking: the page gains a section; every existing request it makes is unchanged.

## Capabilities

### New Capabilities

_None._

### Modified Capabilities

- `notification-preferences`: gains one requirement — the web page lets a signed-in user subscribe to their job outcomes by e-mail through the existing preference routes, without creating a preference the user did not select. Every existing requirement (closed sets, owner scoping, upsert, absence means not subscribed, enrolment boundary, e-mail needs no secret) is unchanged and is what the page relies on.

## Impact

- **Code:** `cmd/video-api/web/index.html`, `cmd/video-api/web/app.js`, `cmd/video-api/web/styles.css`; the existing static-asset test in `cmd/video-api/main_test.go` gains a check that the served page carries the new section and script.
- **APIs / services / dependencies:** none changed.
- **Documentation (finalization):** `README.md`'s "Current Limitations" still says e-mail is not implemented, which is already false and is corrected together with the new page capability; `CLAUDE.md` / `docs/architecture.md` / `docs/flows.md` are updated where they describe what the page calls.
