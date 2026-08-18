#!/usr/bin/env python3
"""PostToolUse hook: after `gh pr create`, wait for Copilot's auto-review to
post, then wake Claude with the review + inline comments so checking them
can't be silently deferred (see repo memory check-pr-comments-before-done)."""
import json
import re
import subprocess
import sys
import time

PR_URL_RE = re.compile(r"https://github\.com/([^/\s]+/[^/\s]+)/pull/(\d+)")
POLL_INTERVAL_SECONDS = 20
MAX_WAIT_SECONDS = 900
API_RETRIES = 3
API_RETRY_DELAY_SECONDS = 5

# GitHub's automatic reviewer app exposes two different `login` values for
# the *same* bot account depending on the endpoint (confirmed against this
# repo's own PRs): "copilot-pull-request-reviewer[bot]" on /reviews, plain
# "Copilot" on /comments. Matching on a substring like "copilot" would also
# let any user whose login merely contains that word (allowed on public
# repos) have their review treated as trusted and injected into the session
# as context, so match on the stable identity behind both aliases instead:
# GitHub Apps always report type "Bot", and html_url is derived from the
# app's real login, which an arbitrary user account cannot obtain.
COPILOT_APP_HTML_URL = "https://github.com/apps/copilot-pull-request-reviewer"


def gh_api(path):
    """Call `gh api <path>`, retrying transient failures. Returns None only
    after every retry is exhausted — callers must not treat that the same
    as a genuinely empty result."""
    for attempt in range(API_RETRIES):
        result = subprocess.run(
            ["gh", "api", path],
            capture_output=True,
            text=True,
            timeout=30,
        )
        if result.returncode == 0:
            try:
                return json.loads(result.stdout)
            except json.JSONDecodeError:
                pass
        if attempt < API_RETRIES - 1:
            time.sleep(API_RETRY_DELAY_SECONDS)
    return None


def is_copilot(user):
    user = user or {}
    return user.get("type") == "Bot" and user.get("html_url") == COPILOT_APP_HTML_URL


def rewake(reason, additional_context):
    print(json.dumps({
        "reason": reason,
        "hookSpecificOutput": {
            "hookEventName": "PostToolUse",
            "additionalContext": additional_context,
        },
    }))
    return 2


def main():
    try:
        payload = json.load(sys.stdin)
    except json.JSONDecodeError:
        return 0

    stdout = (payload.get("tool_response") or {}).get("stdout", "") or ""
    match = PR_URL_RE.search(stdout)
    if not match:
        return 0

    repo, number = match.group(1), match.group(2)
    pr_url = match.group(0)

    elapsed = 0
    while elapsed < MAX_WAIT_SECONDS:
        reviews = gh_api(f"repos/{repo}/pulls/{number}/reviews")
        copilot_reviews = [r for r in (reviews or []) if is_copilot(r.get("user"))]
        if copilot_reviews:
            comments = gh_api(f"repos/{repo}/pulls/{number}/comments")
            comments_fetch_failed = comments is None
            copilot_comments = [
                {"path": c.get("path"), "line": c.get("line"), "body": c.get("body")}
                for c in (comments or [])
                if is_copilot(c.get("user"))
            ]
            review_bodies = "\n\n".join(r.get("body", "") for r in copilot_reviews if r.get("body"))
            lines = [
                f"Copilot posted its automatic review on {pr_url} (PR #{number}).",
                "Check it now before treating this task as done (see check-pr-comments-before-done memory):",
            ]
            if review_bodies:
                lines.append(f"\nReview summary:\n{review_bodies}")
            if comments_fetch_failed:
                lines.append(
                    f"\nCould not fetch inline comments after {API_RETRIES} attempts "
                    f"(gh api call kept failing) — run `gh api repos/{repo}/pulls/{number}/comments` "
                    "manually to check for them."
                )
            elif copilot_comments:
                lines.append(f"\nInline comments ({len(copilot_comments)}):")
                for c in copilot_comments:
                    lines.append(f"- {c['path']}:{c['line']}: {c['body']}")
            else:
                lines.append(
                    f"\nNo inline comments — run `gh pr view {number} --json reviews` "
                    "to read the full review body."
                )
            return rewake(
                f"Copilot review posted on PR #{number} — check it before finishing.",
                "\n".join(lines),
            )

        time.sleep(POLL_INTERVAL_SECONDS)
        elapsed += POLL_INTERVAL_SECONDS

    return rewake(
        f"Copilot review check timed out on PR #{number} — verify manually before finishing.",
        f"No Copilot review was detected on {pr_url} (PR #{number}) after "
        f"{MAX_WAIT_SECONDS // 60} minutes of polling — either it hasn't posted yet, "
        "the `gh api` calls kept failing, or this PR wasn't eligible for the automatic "
        f"review. Check manually before treating this task as done: "
        f"`gh pr view {number} --json reviews` and "
        f"`gh api repos/{repo}/pulls/{number}/comments`.",
    )


if __name__ == "__main__":
    sys.exit(main())
