## 1. Page markup and style

- [x] 1.1 In `cmd/video-api/web/index.html`, add a hidden-by-default "📧 Notificações por e-mail" section after the auth panel: an e-mail input, two checkboxes ("Quando o processamento falhar", "Quando o processamento for concluído"), a save button, a message element, a warning element (hidden by default) for differing stored addresses, and a one-line hint that the notification covers videos whose processing ends after the user first activates it. All copy pt-BR.
- [x] 1.2 In `cmd/video-api/web/styles.css`, style the section consistently with `.auth-panel` (reuse its rules where possible rather than adding a parallel set).

## 2. Page behaviour

- [x] 2.1 In `cmd/video-api/web/app.js`, add `loadNotificationPreferences()`: with no token, hide the section and call nothing; otherwise `GET /api/notification-preferences`, record which `email` preferences exist and are enabled per event type, set the checkboxes, and fill the address from a stored `email` destination (the `video_job.failed.v1` one first), falling back to the account e-mail from `localStorage`. When both `email` preferences exist with different destinations, show the warning that saving applies the address in the field to both; hide it otherwise.
- [x] 2.2 Add `saveNotificationPreferences()`: for each of `video_job.failed.v1` and `video_job.completed.v1`, send `PUT /api/notification-preferences` with `{event_type, channel: "email", enabled, destination}` and no `secret` — when the box is checked, or when it is unchecked and a preference was present on the last read. Send nothing otherwise. Re-read afterwards so the page reflects what is stored.
- [x] 2.3 Map responses to page messages: `401` → `clearSession()` as the other calls do; `400` → "Endereço de e-mail inválido."; `429` → a "muitas requisições, tente novamente em instantes" message with no retry; other failures → a generic error; success → "Preferências salvas.".
- [x] 2.4 Call `loadNotificationPreferences()` on page load, after a successful sign-in, and on sign-out (which hides the section).

## 3. Test

- [x] 3.1 Extend `TestFrontend_StaticRoutes_ServeExpectedContent` in `cmd/video-api/main_test.go` so `/` is asserted to contain the notification section's marker and `/app.js` to contain `/api/notification-preferences`.

## 4. Quality gates and verification

- [x] 4.1 `docker compose run --build --rm app-test go test ./... -v` passes (the diff includes a `.go` file).
- [x] 4.2 Against `docker compose up --build`: sign in, confirm the section shows the account e-mail and no selection; select failure only, save, reload, and confirm the selection persists; confirm through the network panel that exactly one `PUT` was sent.
- [x] 4.3 Still subscribed to failures, upload a file with a valid extension whose content `ffmpeg` cannot decode (e.g. a text file renamed `.mp4`), wait for the page to report the failure, and confirm the failure e-mail in Mailpit (`http://127.0.0.1:8025`).
- [x] 4.4 Deselect failure, save, and confirm one `PUT` with `enabled: false` and none for completion; confirm a normal upload still yields a downloadable ZIP (frontend full-flow non-regression).
- [x] 4.5 `git diff --check`; the four required checks green on the PR.

## 5. Finalization (after the implementation PR merges — not part of it)

- [x] 5.1 Check off the implementation tasks above.
- [x] 5.2 `README.md`: rewrite the "Webhooks are the only notification channel" limitation — e-mail is implemented, and the page subscribes to it; webhook remains API-only.
- [x] 5.3 `CLAUDE.md`, `docs/architecture.md`, `docs/flows.md`: update where they describe what the page calls, so the claim that `app.js` calls `/api/notification-preferences` is true and described.
- [x] 5.4 `npx --yes @fission-ai/openspec validate add-notification-email-preference-ui --strict --no-interactive`, fix every error, then archive.
