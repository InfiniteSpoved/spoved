---
name: issue-to-merge
description: Take a spoved issue the person names to merge-ready pull requests by splitting it into work items, delegating them to subagents, and running fresh quality, security, UX and red-team reviews before saying it is ready. Use when someone says "do issue #N", "get #N ready for merge" or "delegate #N".
---

# Issue to merge

The person gives an issue number. You lead: you split the work, delegate it, check what comes back and drive every pull request to merge-ready. The `coder` skill is the procedure for one pull request; this skill runs it for a whole issue with subagents. Read `CLAUDE.md` (conventions and Learnings) and `CONTRIBUTING.md` first.

The questions the person asks most often are "is everything in #N done?", "did you really test it?" and "is the PR ready to merge?". Work so that you can answer each one with evidence before they ask.

## 1. Read and split

- Read the issue and every comment (`mcp__github__issue_read`); maintainer comments are requirements. Read linked issues and pull requests that it builds on.
- Check `develop` for each requirement before planning it: issue text goes stale, and parts may already be built. Say which parts are already done.
- Write the requirements as a numbered list taken from the issue. Every later step refers to it, and the final report marks each one done, moved to a follow-up issue, or dropped with the person's OK.
- Split the work into items, each one reviewable pull request (or one commit inside a pull request when the person asked for a single PR). For each item write: goal, the requirements it covers, files and functions, what stays out of scope, the tests that prove it, and which items it depends on.
- Ask the person only about choices that change the goal, an API, a schema, permissions or a default. Recommend an option and keep working on it meanwhile. Save their decisions to project memory so later threads don't ask again.
- When the feature copies a competitor (GitHub, GitLab, Bitbucket, Azure DevOps), write down how theirs behaves and where spoved will differ. The person will ask "does it work exactly like X?".
- Post the plan on the issue, keep the list of items as your progress checklist, and say which item you start with.

## 2. Design UI before building it

If an item adds or changes screens, share mockups first (a design doc or canvas with the screens, the empty and error states, and open questions with your recommendation) and wait for the person's UX review before building those screens. Backend items can go ahead meanwhile. Put each control where users of GitHub and GitLab look for it, not only in settings.

## 3. Delegate

- Give each item to a subagent (Agent tool) with a self-contained brief: the issue number, the item's goal, requirements and tests, the files to start from, the relevant `CLAUDE.md` Learnings, and the rules below. Run independent items in parallel, each in its own worktree with a short path (`/home/user/wt-<x>`, long paths break the integration and e2e sockets). Run dependent items in order.
- The rules every brief carries: branch off the latest `origin/develop`; `ponytail` at full level; tests first and never remove or skip one; `git commit -s`, stage files by name, no `package-lock.json`; run the local checks of `CLAUDE.md` "Testing" for the change; report what was run, what wasn't, and every failure.
- Read each result yourself: the diff, the tests and the check output. A subagent's "done" is a claim; re-run anything that looks thin.

## 4. Test it the way it will be used

Automated tests passing is not "tested". Before a pull request is ready, also:

- **Run it for real.** Build the branch (a production build for UI: `RUN_MODE = prod`, embedded assets), start it on a fresh database and drive the feature as a user would, through the web and the API. Say what you did and what you saw.
- **Projects.** Repeat the main flow on a repository inside a project (`org/project/repo`): names, links, confirmations and lookups that assume `owner/repo` break there.
- **Phones.** For UI, use the pages at 390 px with touch, not only screenshots: menus and prompts that close on focus loss, hidden buttons and on-screen keyboards have broken flows that passed desktop e2e.
- **Concurrency.** When the change runs on a push, merge, cron or queue path, test the races (a branch that moves mid-operation, two runs at once). If the suite can't reach a case, add a test hook rather than calling it untestable. For hot paths, run a stress test with many users in parallel and report lost or failed operations.
- **Deployment.** If the change adds files outside the binary, a setting or a service, check that the release image and the deployment pipelines ship it, and say what an operator has to do.

## 5. Open the pull request

- Branch off `develop` (the session's assigned branch if it has one), target `develop`, and fill in `.github/pull_request_template.md`: the requirements this PR covers, the tests added and run (automated and by hand), what was not tested, and threat notes for anything security-relevant.
- Use `Fixes #N` only in the pull request that finishes the issue, `Part of #N` in the others. GitHub closes the issue only on `Fixes`/`Closes`.
- Start the body with the attribution block the session requires, then assign the person and request their review (`issue_write` with `assignees`, `update_pull_request` with `reviewers`; GitHub refuses the review request when the person is the author, so skip it then).
- Open it as a draft until the reviews below are done. Subscribe to its activity and tell the person it is up, with the link.

## 6. Review with fresh agents

Before calling a pull request ready, start four fresh agents pinned to the strongest model (Agent `model: "opus"`). Give each the pull request number and the diff, not your conclusions, so their read isn't anchored on yours:

1. **Quality**: correctness, sustainable and robust design, missing tests, reuse of existing helpers, `CLAUDE.md` conventions and Learnings, smallest diff, and that every requirement the PR claims is met.
2. **Security**: the `security-review` skill on the diff (permissions, hidden = missing, data exposure, injection, redirects, outbound requests, secrets, audit).
3. **UX**, when there is UI: the `screenshots` skill (desktop and mobile, light and dark) and "is this the right experience?": can users find it, does the preview match what the action does, does a wide-scope destructive action ask for a stronger confirm, do the words read well (plurals, "1 branch"), is it consistent with GitHub/GitLab. Attach the screenshots to the pull request.
4. **Red team**: try to break it as an attacker on a running instance from the branch (anonymous, other users, tokens with less scope, hidden repos, crafted input, no shell or git CLI reachable); report what got through with the exact steps.

Fix every finding, or answer it with the reason it isn't one, then re-run the agent whose findings changed code. Follow up the lower-confidence notes too: past reviews found real bugs there. Post the findings as one review with `event: COMMENT`: reviews post under the person's account, and GitHub refuses approve or request changes on one's own pull request.

A real vulnerability is never written in this repository. File it as an issue in `InfiniteSpoved/spovedOps` (private) with the details, and keep the spoved commit, pull request and comments neutral (they may name the spovedOps issue by number only).

## 7. Keep it green

- CI's **CI success** check must be green on the head. On a failure, read the job log, reproduce locally, fix the cause and push; "flaky" is not a cause. Failures known to be environmental are listed in `CLAUDE.md`; anything else is checked on `origin/develop` before calling it unrelated.
- Check `develop` for new commits at every event, without waiting to be told. When it moves, merge `origin/develop` into the branch with a merge commit (never rebase or force-push a pushed branch), then re-test where the new commits meet yours (shared pages, forms, settings).
- Answer every review comment: fix it or explain why not.

## 8. Ready to merge

A pull request is ready when CI success is green on its head, it contains the latest `develop`, the step 4 checks and all four reviews are done, and every review thread is answered. Then tell the person in one short message that it is ready to merge, with the link, what was tested (and what wasn't) and what the reviews found.

When the person has approved it (or said to merge once approved and green), check again right before merging, because other pull requests merge in parallel: `git fetch origin develop` and `git merge-base --is-ancestor origin/develop HEAD`. If develop has moved, merge it in (merge commit, never a rebase), re-run the checks for the files where both sides meet, push, and wait for CI success on the new head before merging. Merge only a head that contains the develop you just fetched and is green. Then merge it with a merge commit (`merge_method: "merge"`). Then watch the CI run on `develop` that the merge starts: a new test can move others into another shard and expose an order-dependent test. If it goes red, fix it in a new pull request right away. Then start the next work item from the new `develop`.

## 9. Close the issue

When the last item is merged, go through the requirements list from step 1 and the issues linked from the issue, including follow-ups opened during the work. Tell the person what is done, what is in an open follow-up issue, and anything left. Never say "everything is done" without that check. Then close the issue with a comment listing the pull requests, unless a `Fixes` already closed it.

## Never

- Never push to `develop`, rebase or force-push a pushed branch, or edit `.github/ci/*` or `.github/workflows/*` in a feature pull request.
- Never merge without the person's approval and a green CI success.
- Never put a vulnerability, secret, credential, host name or infrastructure detail in this repository's issues, pull requests, commits, branch names or comments.
