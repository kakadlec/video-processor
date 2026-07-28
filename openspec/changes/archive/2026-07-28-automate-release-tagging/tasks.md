## 1. Config files

- [x] 1.1 Add `release-please-config.json` at repo root: `release-type: simple`, package `.`.
- [x] 1.2 Add `.release-please-manifest.json` at repo root: `{".": "0.0.0"}`.

## 2. Workflow

- [x] 2.1 Add `.github/workflows/release-please.yml`: trigger on push to `main`, `permissions: contents: write, pull-requests: write`, single job running `googleapis/release-please-action@v5` with `release-type: simple` (config/manifest picked up from repo root by default).

## 3. Documentation

- [x] 3.1 Update `CLAUDE.md`: document the Conventional Commits requirement (with examples) and the release process (merge the bot-maintained Release PR; never hand-create tags).

## 4. Verification

- [x] 4.1 Commit and push this change using a Conventional Commit message.
- [x] 4.2 Confirm on GitHub that the `release-please` workflow ran successfully and opened (or would open) a Release PR — check real workflow output, don't just assume the config is correct.
