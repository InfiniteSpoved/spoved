# Working on this fork

`GitHatOrg/GitHat` is the repository of CodeHat, a private fork of Codeberg `forgejo/forgejo`.

- `main` mirrors upstream. `.github/workflows/sync-upstream.yml` fast-forwards it nightly, so never push to it.
- `develop` is the default branch. All PRs target `develop`. Merge `origin/develop` into feature branches with a merge commit; don't rebase pushed branches.
- Use the `ponytail` skill (`.claude/skills/ponytail`) at full level: smallest correct diff, reuse existing helpers and patterns, no unrequested abstractions. It never removes tests, validation or security checks the task asks for.

## Environment (cloud containers)

- You run as root: export `GITEA_I_AM_BEING_UNSAFE_RUNNING_AS_ROOT=true` for Go tests and the server.
- Playwright: never run `playwright install`. If the browser isn't found, temporarily add `launchOptions: {executablePath: '/opt/pw-browsers/chromium'}` in `playwright.config.ts` and revert it before committing. Only Chromium is installed; CI uses Firefox.
- Run `make frontend` before e2e tests, or pages render without assets.
- Run `make generate-ini-sqlite` once, then use `PROJECT_CONF=tests/sqlite.ini` for migration tests.
- The Node version is older than `.node-version`, so `lockfile-check` fails here. Ignore it unless you changed package files.

## Testing (local equivalent of CI, `.github/ci/job.sh`)

1. Backend: golangci-lint on the changed packages, `make lint-go-vet lint-locale lint-locale-usage lint-swagger lint-editorconfig`, and `make fmt` leaves no diff.
2. Frontend, if templates/JS/CSS changed: `make lint-frontend checks-frontend test-frontend frontend`.
3. Unit: `go test -tags 'sqlite sqlite_unlock_notify'` on the changed packages and their neighbours.
4. Migrations: `PROJECT_CONF=tests/sqlite.ini go test ./models/forgejo_migrations/...`. Never run this at the same time as the integration suite, because both rewrite the shared git hooks.
5. Integration: the full suite in one run, `make test-sqlite` (about 45 min; run it in the background and grep `^(--- FAIL|FAIL|ok)`). The tests depend on shared global state and order, so never shard the suite or run it partially to judge a PR. Single tests: `make 'test-sqlite#TestName'`.
6. E2E: `make 'test-e2e-sqlite#<spec>.test.e2e'` for the specs whose `@watch` covers your files.

These fail in containers for environment reasons only. They also fail on develop and are not caused by a PR:
- Integration (no `ssh`/`ssh-keygen`, and the root warning appears in CLI output): `TestActions_CmdForgejo_Actions`, `TestDeployKeyBranchProtection`, `TestDeployKeyTagProtection`, `TestGit`, `TestRepoSSHSignedTags`, `TestInstanceSigning`, `TestPushDeployKeyOnEmptyRepo`, `TestKeyOnlyOneType`, `TestRepoWikiGitOperation`, `TestProtectedBranchDeleteForcePushSSH`, and any new test that pushes over SSH. Cover SSH-only logic with a hook-level or HTTP test as well.
- E2E: `webauthn.test.e2e.ts`.
Before calling any other failure environmental, rerun it on `origin/develop`.

## CI (runs in InfiniteSpoved/spovedBuild)

This repository has no CI workflow. CI runs in the public InfiniteSpoved/spovedBuild repository (its `ci.yml` runs `.github/ci/job.sh` from here) and sets the commit status "CI success" on the tested commit. Its public logs show only pass/fail; each job's output is uploaded encrypted. Nothing that describes this repository may appear there.

- After pushing a PR branch, start one run for the pushed commit. Pass the SHA, never a branch name: the `ref` input is printed in the public log.
  `gh api -X POST repos/InfiniteSpoved/spovedBuild/actions/workflows/ci.yml/dispatches -f ref="$(gh api repos/InfiniteSpoved/spovedBuild --jq .default_branch)" -f 'inputs[repo]=InfiniteSpoved/spoved' -f "inputs[ref]=$(git rev-parse HEAD)" -f "inputs[log-key]=$(cat <scratchpad>/ci-key.b64)"`
- That runs the lints, frontend checks and unit tests (about 10 min). Add `-f 'inputs[full]=true'` for the migration, integration and E2E tests too (about an hour). A full run is pre-approved for every pushed head of a PR that changes code (user decision 08 Oct); docs/CSS-only PRs use the quick run. A green "CI success" on the PR head is a merge gate.
- To iterate on a fix, add `-f 'inputs[only]=unit,e2e'` (comma-separated stages: lint, frontend, unit, migrations, integration, e2e; with full=true for the last three) to run only those jobs. Such a run reports "CI partial", never "CI success", so the merge gate stays a full run on the head. Only stage names go in it, never test names.
- Log key, once per session: `export GNUPGHOME=<scratchpad>/gnupg; mkdir -m 700 -p $GNUPGHOME; gpg --batch --passphrase '' --quick-gen-key ci-log default default 30d; gpg --export ci-log | base64 -w0 > <scratchpad>/ci-key.b64`.
- Find your run with `gh api 'repos/InfiniteSpoved/spovedBuild/actions/runs?event=workflow_dispatch&per_page=5'` (check `head_sha` is spovedBuild's and the creation time is yours), wait with a background loop on its `status`, then read a failed job's log: `gh run download <run id> -R InfiniteSpoved/spovedBuild -n log-<N>` for "Job N" (`log-plan` lists the jobs in order), and `gpg -d log-<N>/log.gpg`.
- Never post logs, test names or code in spovedBuild (issues, comments, run names).

## Conventions

- Migrations go in `models/forgejo_migrations/v18<letter>_<desc>.go`. The full file name is the migration ID, so a shared prefix is fine, but pick the next free letter so they run in order. Check `ls models/forgejo_migrations` first; if develop gained a later letter while you worked, rename yours after it.
- New UI strings go in `options/locale_next/locale_en-US.json`.
- After API changes, run `make generate-swagger`. Never edit `templates/swagger/v1_json.tmpl` by hand.
- Merge conflicts in the locale file: keep both sides. Swagger conflicts: regenerate.
- CSRF: web POSTs are protected by `http.NewCrossOriginProtection` in `routers/web/web.go`; forms don't need a token.
- Team memberships record why they exist (`team_user.reason`, e.g. `MembershipReasonAddedByUserGroup`); user-group memberships record the login source that synced them (`user_group_user.login_source_id`, 0 = added by an admin). Only remove what your code path added.

## Learnings

Add a line here when you hit something the next person should know.

- Grouped counts: use one `GROUP BY` query per list (see `countGroupedBy` in `models/organization/user_group.go`), not one query per row.
- Deploy keys are referenced by `PublicKey` ID (`DeployKey.KeyID`) in protection rules, so org deploy keys can reuse the same lists later.
- A deploy-key push runs with `opts.UserID` set to the repository owner; decide deploy-key permissions from the key (`ctx.deployKeyKeyID` in the pre-receive hook), never from that user ID.
- `make 'test-sqlite#A|B'` breaks on the pipe; after one build, run `PROJECT_ROOT=$PWD PROJECT_CONF=tests/sqlite.ini ./integrations.sqlite.test -test.run 'A|B'`.
- Integration tests: build expected absolute links from `setting.AppURL`, not from the `onApplicationRun` callback URL; the hosts differ (`localhost` vs `127.0.0.1`).
- The container's /usr/local/bin/golangci-lint is built with an older Go and can't load the config ("Go language version ... lower than targeted"). Build the Makefile-pinned version once: `GOTOOLCHAIN=go1.27.1 GOBIN=<scratch> go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@<GOLANGCI_LINT_PACKAGE version>`, then run it with `--build-tags 'sqlite sqlite_unlock_notify'`.
- Integration tests that call the runner RPC (`newMockRunner`/`Register`) need `onApplicationRun`; with only `tests.PrepareTestEnv`, nothing listens on the port and you get "connection refused".
- `make lint-css` runs stylelint on every file under `web_src/css`, including `.js`, so JS tests for CSS (e.g. `web_src/js/company-contrast.test.js`) must live under `web_src/js`.
- Integration tests run the queues `immediate`, so `StartPRCheckAndAutoMerge` merges synchronously: assert right after the call (no `Eventually`), and mock the clock with `timeutil.MockSet`/`defer timeutil.MockUnset()`.
- `git log --format=%B` cuts a commit message at the first NUL byte, so message checks only see the text before it.
- To check the files a merge commit introduces itself (an "evil merge"), run `git diff-tree --stdin -r --diff-merges=first-parent` over all new SHAs, merges included.
- E2E under a git worktree in the long scratchpad path fails with "bind: invalid argument" (the unix socket path is too long). Run e2e in the main checkout.
- After switching branches across a migration, delete `tests/e2e/forgejo-e2e-sqlite` before running e2e, or the server refuses the newer database.
- E2E `repo-settings.test.e2e.ts` › `deploy key pickers › tag protection` [Mobile Chrome] also fails on develop in containers (a cell intercepts the Remove click); it's environmental.
- `make lint`/`make frontend` run npm with the container's older Node and rewrite package-lock.json. Stage files by name (never `git add -A`) and keep package-lock.json out of commits unless you changed package files.
- To backdate Actions rows in tests, insert them and then run `UPDATE action_run SET created = ?`. `ActionRun.Created` is `xorm:"created"`, so a value set on insert is overwritten. Also give each inserted `ActionRunJob` its own `Handle`, because that column is unique.
- Creating issues, commenting, reacting, opening PRs, AGit pushes (`refs/for/*`), email replies and cross-references need only READ permission. So a role that raises access to read must do it only at read-only entry points (GET/HEAD, pull, LFS download), never inside GetUserRepoPermission.
- `rendering-iframe.test.e2e.ts` is flaky on Chromium in containers: the iframe height sometimes stays at the 300px default past the 3 s `expect.poll`, and a different test fails on each run, on develop too. Rerun it alone before calling a failure real.
- `make 'test-e2e-sqlite#…'` depends on the `playwright` target, which runs `npx playwright install` and downloads browsers. To use only the pre-installed Chromium, build once, then run `./e2e.sqlite.test` directly with the temporary `executablePath`.
- `make test-backend` in containers also fails, identically on develop: `models/asymkey` (Test_SSHParsePublicKey, Test_calcFingerprint), `modules/jwtx` TestCannotCreatePrivateKey, `modules/setting` TestLogPrepareFilenameForWriter, `modules/storage` TestMinioCredentials. They are environmental (root, no ssh tools, no minio); ignore them unless your PR touches those packages.
- Azure storage tests: run `npx -y -p azurite@3.37.0 azurite-blob --inMemoryPersistence --skipApiVersionCheck --blobHost 127.0.0.1` (the binary lives in the `azurite` package; without the skip flag the SDK's newer API version gets a 400), then set `TEST_AZURITE_URL=127.0.0.1:10000`.
- Keep `github.com/Azure/azure-sdk-for-go/sdk/storage/azblob` at v1.7.x: v1.8+ pulls in apache/arrow-go and bumps unrelated modules. Add Go modules with an explicit `@version`, never `@latest`, and review the go.mod diff.
- Auth Verify methods don't check IsActive/ProhibitLogin; that check runs after the rate limiter. So any per-user decision made in middleware before it (e.g. `doer.IsAdmin` in `checkRateLimit`) must check `IsActive && !ProhibitLogin` itself.
- Code map: run `tools/codemap.sh` once per session, then `tools/codemap.sh query "<question>"` before grepping and `tools/codemap.sh update` after changes (pinned, offline graphify; see `.claude/skills/codemap`).
- Changing the Actions run-view JSON (`ViewJob` and friends in routers/web/repo/actions/view.go) also needs the expected maps in tests/integration/actions_view_test.go updated; run `make 'test-sqlite#TestActionViews'`, as the unit tests don't cover them.
- Never let a panic reach `net/http/fcgi`: it has no recover, so under `PROTOCOL=fcgi` a panic (including `http.ErrAbortHandler`) kills the process. `runFCGI` recovers `ErrAbortHandler`, but fcgi can't drop the connection: an aborted stream ends with a normal end-of-request there, so the client gets a truncated body that looks complete.
- `IsLastAdminUser` counts the *other* usable admins (active, login allowed, individual) and never reads the user's own flags, because `UpdateUser` changes them in memory before the check. In fixtures user 1 is the only admin; to test last-admin cases, make another user admin with `UpdateUserCols`, and use user 8 when the test deletes it (no orgs or repos).
- `repo-diff.js` AJAX-submits every `form` inside `.conversation-holder`; a plain-POST form placed in a code conversation (e.g. the comment context menu) must be excluded from that selector (`:not(.create-pull-task)`), or submitting it throws a JS error.
- An AGit PR's `HeadBranch` is "<pusher>/<topic>", not a real branch, and its HeadRepo is the base repo: any code that deletes, restores or reads `pr.HeadBranch` as a branch must skip `pr.Flow == issues_model.PullRequestFlowAGit`.
- `LoadFixtures` doesn't reset SQLite's `sqlite_sequence` (or MySQL AUTO_INCREMENT) and the integration DB persists between runs, so a test that inserts an explicit huge ID (e.g. 1<<53+1) must delete the row and reset the counter, or every later insert gets a huge ID too (see the "ID above 2^53" subtest in `api_repo_actions_test.go`).
- Code-owner checks: read CODEOWNERS from the PR's base branch (never the head or the default branch) and require an approving owner for every matching line; the union of matching lines lets a catch-all owner approve a specific path.
- Org branch protection rules live in protected_branch with repo_id=0 and org_id set; enforcement must use GetMatchedProtectedBranchRules and pass every returned rule, and a by-ID query on protected_branch must always include repo_id or org_id (xorm drops zero fields).
- Inside a shared tx, `models.RemoveTeamMember` failing with `ErrLastOrgOwner` aborts the whole outer tx (its `db.TxContext` closer rolls back) even if you handle the error: check `team.IsOwnerTeam() && team.NumMembers == 1` first, and reuse one `*Team` per org so `NumMembers` stays current.
- Org archive: `context.IsArchivedOrgWrite` refuses non-GET requests to an archived org and its repos in `HandleOrgAssignment`/`orgAssignment` and `RepoAssignment`/API `RepoAccess`, with route-pattern exemptions (unarchive, org delete); repo creation and transfers are checked in `CreateRepositoryByExample` and `TransferOwnership`. Group/LDAP/OAuth sync skips adds to archived orgs but still applies removals, and callers run `usergroup.SyncOrgTeams` after unarchive (`services/org` can't import `services/usergroup`).
- Product name: to make an English ini string say CodeHat, add the same key to `options/locale_next/locale_en-US.json` (locale_next wins over the ini), never edit the ini; list it in `codehatShadowKeys` (`modules/translation/codehat_test.go`) and `docs/branding.md`. `test.BrandMentions` (used by `TestBranding` and the install/500/mail tests) fails on any visible "Forgejo"/"GitHat"; allow a legitimate upstream one only by exact phrase in `brandAllowed`, with a comment.
- Maintenance mode: check `setting.GetMaintenance(ctx).ReadOnly()` (app.ini `[maintenance] MODE` wins; an expired end time reads as off) at the shared chokepoint, not per caller. A new POST/PUT route that only reads, or an admin setting that must stay usable, goes in the exact "METHOD pattern" lists in `routers/common/maintenance.go` (TestMaintenanceAllowed checks every entry exists). The CLI's `initDB` sets the settings dyn getter, so CLI code can read `setting.Config()` values; after a CLI write in a test, call `config.GetDynGetter().InvalidateCache()`, since the CLI runs in another process.
- Client IP: read it with `common.ClientIP(req)` (`routers/common/middleware.go`) after `ProtocolMiddlewares`. `joinForwardedFor` makes a trusted proxy's appended X-Forwarded-For win over X-Real-IP, and when the two disagree it marks the IP ambiguous (`ClientIP` returns an invalid address), which the IP allowlist refuses. SSH passes the client IP to `/api/internal/serv` from `SSH_CONNECTION`, which the built-in server always sets; serv refuses a missing one while SSH is enforced.
- Effective access: `services/accessreport` replays `GetUserRepoPermission` on bulk-loaded data, and `TestComputeMatchesGetUserRepoPermission` checks every fixture user × repo against it; if you change the permission code, update the replay too. Note that the `access` table drops read rows on public repos (`applyMinVisibility`), so a missing row means "read unless restricted", not "none".
- Email recipient filtering goes through `services/mailer` `ShouldEmail`/`emailFilter` (one batched `LoadNotificationSettings` per mail); a new mail path passes a `user_model.NotifyEvent` and the filter never adds recipients.
- A counting xorm hook (e.g. for query-count tests) must return `db.TracingHook{}.BeforeProcess(c)`, not `c.Ctx`: xorm keeps only the last hook's context, and TracingHook's AfterProcess panics without its task. Warm up once before counting, since `ToUser` saves a generated avatar the first time it renders a new user.
- E2E `user-settings.test.e2e.ts` › "User: Add authorized integration (actions local)" also fails on develop in containers; it's environmental.
- Trash: lookups and `GetUserRepoPermission` hide repos with `trashed_unix != 0`. Queries that reach repos by an id column add `repo_model.NotTrashedCond(col)`, and code that must see trashed repos (owner deletion, adopt, purge) uses `IncludeTrashed` or `GetRepositoryByIDIncludingTrashed`. `CountRepositories` counts trashed repos on purpose: it is the F1 guard that refuses owner deletion. Repo creation and fork limits go through `repo_model.CheckRepoCreationLimit`.
- zoekt: a non-delta `Builder.Finish` deletes the repo's old shards (the only way a branch leaves the index), while a delta build fails with a branch-set error if the branches differ, so a repo with extra branches is always rebuilt whole (`IndexBranches`). `query.NewAnd` doesn't flatten, so tests must walk nested `And` nodes.
- OAuth2 app policy: `oauth2app.MayManage(ctx, doer, ownerID)` (`services/auth/oauth2app`) is the one check for creating or changing (name, redirect URIs, secret) an OAuth2 app; every web handler in `OAuth2CommonHandlers`, API POST/PATCH `/user/applications/oauth2` and any new route must call it before any write. An unset `oauth2.app_policy` means site admins only. In tests that store a system setting, fixtures reset the settings revision, so two tests can write different values under the same revision and the cached one wins: call `bumpSettingsRevision(t)` (`tests/integration/settings_revision_test.go`, one counter for every test) after each write to a system setting, including one made through a web or API request.
