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


def gh_api(path):
    result = subprocess.run(
        ["gh", "api", path],
        capture_output=True,
        text=True,
        timeout=30,
    )
    if result.returncode != 0:
        return None
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError:
        return None


def is_copilot(login):
    return bool(login) and "copilot" in login.lower()


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
        reviews = gh_api(f"repos/{repo}/pulls/{number}/reviews") or []
        copilot_reviews = [r for r in reviews if is_copilot((r.get("user") or {}).get("login"))]
        if copilot_reviews:
            comments = gh_api(f"repos/{repo}/pulls/{number}/comments") or []
            copilot_comments = [
                {"path": c.get("path"), "line": c.get("line"), "body": c.get("body")}
                for c in comments
                if is_copilot((c.get("user") or {}).get("login"))
            ]
            review_bodies = "\n\n".join(r.get("body", "") for r in copilot_reviews if r.get("body"))
            lines = [
                f"Copilot posted its automatic review on {pr_url} (PR #{number}).",
                "Check it now before treating this task as done (see check-pr-comments-before-done memory):",
            ]
            if review_bodies:
                lines.append(f"\nReview summary:\n{review_bodies}")
            if copilot_comments:
                lines.append(f"\nInline comments ({len(copilot_comments)}):")
                for c in copilot_comments:
                    lines.append(f"- {c['path']}:{c['line']}: {c['body']}")
            else:
                lines.append(
                    f"\nNo inline comments — run `gh pr view {number} --json reviews` "
                    "to read the full review body."
                )
            print(json.dumps({
                "reason": f"Copilot review posted on PR #{number} — check it before finishing.",
                "hookSpecificOutput": {
                    "hookEventName": "PostToolUse",
                    "additionalContext": "\n".join(lines),
                },
            }))
            return 2

        time.sleep(POLL_INTERVAL_SECONDS)
        elapsed += POLL_INTERVAL_SECONDS

    return 0


if __name__ == "__main__":
    sys.exit(main())
