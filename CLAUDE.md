# Working on this fork

`lilayam/forgejo` is a private fork of Codeberg `forgejo/forgejo`.

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

GitHub Actions credits are exhausted. PR jobs fail within seconds and their logs return 404. They never got a runner, so ignore them and rely on the local run.

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
- `make lint-css` runs stylelint on every file under `web_src/css`, including `.js`, so JS tests for CSS (e.g. `web_src/js/company-contrast.test.js`) must live under `web_src/js`.
