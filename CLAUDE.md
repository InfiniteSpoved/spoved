# Working on this fork

`GitHatOrg/GitHat` is a private fork of Codeberg `forgejo/forgejo`.

- `main` mirrors upstream. `.github/workflows/sync-upstream.yml` fast-forwards it nightly, so never push to it.
- `develop` is the default branch. All PRs target `develop`. Merge `origin/develop` into feature branches with a merge commit; don't rebase pushed branches.
- Use the `ponytail` skill (`.claude/skills/ponytail`) at full level: smallest correct diff, reuse existing helpers and patterns, no unrequested abstractions. It never removes tests, validation or security checks the task asks for.

## Environment (cloud containers)

- You run as root: export `GITEA_I_AM_BEING_UNSAFE_RUNNING_AS_ROOT=true` for Go tests and the server.
- Playwright: never run `playwright install`. If the browser isn't found, temporarily add `launchOptions: {executablePath: '/opt/pw-browsers/chromium'}` in `playwright.config.ts` and revert it before committing. Only Chromium is installed; CI uses Firefox.
- Run `make frontend` before e2e tests, or pages render without assets.
- Run `make generate-ini-sqlite` once, then use `PROJECT_CONF=tests/sqlite.ini` for migration tests.
- The Node version is older than `.node-version`, so `lockfile-check` fails here. Ignore it unless you changed package files.

## Testing (local equivalent of `.github/workflows/ci.yml`)

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

GitHub Actions minutes are limited. PR CI runs only the lints, frontend checks and unit tests. Migration, integration and E2E tests run on GitHub only when someone starts the CI workflow by hand, so the local run stays the gate for them. Never start that manual run without the user's OK, and push as few times per PR as you can: every push uses about an hour of CI.

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
