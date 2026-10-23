---
name: issue-to-merge
description: Take a spoved issue the person names to merge-ready pull requests by splitting it into work items, delegating them to subagents, and running fresh quality, security, UX and red-team reviews before saying it is ready. Use when someone says "do issue #N", "get #N ready for merge" or "delegate #N".
---

# Issue to merge

The person gives an issue number. You lead: you split the work, delegate it, check what comes back and drive every pull request to merge-ready. The `coder` skill is the procedure for one pull request; this skill runs it for a whole issue with subagents. Read `CLAUDE.md` (conventions and Learnings) and `CONTRIBUTING.md` first.

## 1. Read and split

- Read the issue and every comment (`mcp__github__issue_read`); maintainer comments are requirements. Read linked issues and pull requests that it builds on.
- Split it into work items, each one reviewable pull request (or one commit inside a pull request when the person asked for a single PR). For each item write: goal, files and functions, what stays out of scope, the tests that prove it, and which items it depends on.
- Ask the person only about choices that change the goal, an API, a schema, permissions or a default. Recommend an option and keep working on it meanwhile.
- Post the plan on the issue, keep the list of items as your progress checklist, and say which item you start with.

## 2. Delegate

- Give each item to a subagent (Agent tool) with a self-contained brief: the issue number, the item's goal and tests, the files to start from, the relevant `CLAUDE.md` Learnings, and the rules below. Run independent items in parallel, each in its own worktree with a short path (`/home/user/wt-<x>`, long paths break the integration and e2e sockets). Run dependent items in order.
- The rules every brief carries: branch off the latest `origin/develop`; `ponytail` at full level; tests first and never remove or skip one; `git commit -s`, stage files by name, no `package-lock.json`; run the local checks of `CLAUDE.md` "Testing" for the change; report what was run and every failure.
- Read each result yourself: the diff, the tests and the check output. A subagent's "done" is a claim; re-run anything that looks thin.

## 3. Open the pull request

- Branch off `develop` (the session's assigned branch if it has one), target `develop`, and fill in `.github/pull_request_template.md` with `Fixes #N` or `Part of #N`, the tests added and run, and threat notes for anything security-relevant.
- Start the body with the attribution block the session requires, then assign the person and request their review (`issue_write` with `assignees`, `update_pull_request` with `reviewers`).
- Subscribe to the pull request's activity and tell the person it is up, with the link.

## 4. Review with fresh agents

Before calling a pull request ready, start four fresh agents pinned to the strongest model (Agent `model: "opus"`). Give each the pull request number and the diff, not your conclusions, so their read isn't anchored on yours:

1. **Quality**: correctness, missing tests, reuse of existing helpers, `CLAUDE.md` conventions and Learnings, smallest diff.
2. **Security**: the `security-review` skill on the diff (permissions, hidden = missing, data exposure, injection, redirects, outbound requests, secrets, audit).
3. **UX**, when there is UI: the `screenshots` skill (desktop and mobile, light and dark), wording and consistency with GitHub/GitLab conventions; attach the screenshots to the pull request.
4. **Red team**: try to break it as an attacker on a running instance from the branch (other users, tokens with less scope, hidden repos, crafted input); report what got through with the exact steps.

Fix every finding, or answer it with the reason it isn't one, then re-run the agent whose findings changed code. Post the findings as one review with `event: COMMENT`: reviews post under the person's account, and GitHub refuses approve or request changes on one's own pull request.

A real vulnerability is never written in this repository. File it as an issue in `InfiniteSpoved/spovedOps` (private) with the details, and keep the spoved commit, pull request and comments neutral (they may name the spovedOps issue by number only).

## 5. Keep it green

- CI's **CI success** check must be green on the head. On a failure, read the job log, reproduce locally, fix the cause and push; "flaky" is not a cause. Failures known to be environmental are listed in `CLAUDE.md`; anything else is checked on `origin/develop` before calling it unrelated.
- When `develop` moves, merge `origin/develop` into the branch with a merge commit; never rebase or force-push a pushed branch.
- Answer every review comment: fix it or explain why not.

## 6. Ready to merge

A pull request is ready when CI success is green on its head, it contains the latest `develop`, all four reviews are addressed and every review thread is answered. Then tell the person in one short message that it is ready to merge, with the link and what was reviewed.

When the person has approved it (or said to merge once approved and green), merge it with a merge commit (`merge_method: "merge"`), then start the next work item from the new `develop`. When the last item is merged, say the issue is done and close it if no pull request did.

## Never

- Never push to `develop`, rebase or force-push a pushed branch, or edit `.github/ci/*` or `.github/workflows/*` in a feature pull request.
- Never merge without the person's approval and a green CI success.
- Never put a vulnerability, secret, credential, host name or infrastructure detail in this repository's issues, pull requests, commits, branch names or comments.
