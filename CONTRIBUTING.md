# Contributing to spoved

spoved is a modified version of [Forgejo](https://forgejo.org), distributed under the GPL-3.0-or-later (see [`LICENSE`](LICENSE) and GPLv3 §5). It is not affiliated with or endorsed by the Forgejo project. Please follow the [Code of Conduct](CODE_OF_CONDUCT.md).

The product name is always written in lowercase: **spoved**.

## Where things go

- Bugs and feature requests: the [issue tracker of this repository](https://github.com/InfiniteSpoved/spoved/issues).
- Security problems: see [`SECURITY.md`](SECURITY.md). Do not open a public issue.
- Do not report spoved changes or bugs to the Forgejo project, and do not propose spoved changes to it. Forgejo's own issue trackers, chat rooms and contribution rules do not apply here.

## Branches

- `develop` is the default branch. Open all pull requests against `develop`.
- `main` mirrors Forgejo and is fast-forwarded by an automated workflow. Never push to it.
- Merge `develop` into your branch with a merge commit when it falls behind. Do not rebase or force-push a branch that is already pushed.
- Changes that touch files inherited from Forgejo (for example `CONTRIBUTING.md`, `README.md`, templates) can conflict when `main` is merged into `develop`; keep such edits small.

## Building and testing

You need Go (the version in `go.mod`), Node.js (the version in `.node-version`), `make` and Git.

```sh
make deps
make build                 # frontend and backend
```

Before opening a pull request, run what applies to your change:

- Backend: `make fmt` (must leave no diff), `make lint-go lint-go-vet lint-locale lint-locale-usage lint-swagger lint-editorconfig`.
- Frontend (templates, JS, CSS): `make lint-frontend checks-frontend test-frontend frontend`.
- Unit tests: `go test -tags 'sqlite sqlite_unlock_notify' ./path/to/package/...` for the packages you changed and their neighbours.
- Integration tests: `make test-sqlite`. Run the whole suite; the tests share state and depend on order, so do not shard it. A single test: `make 'test-sqlite#TestName'`.
- End-to-end tests: `make 'test-e2e-sqlite#<spec>.test.e2e'` for the specs that cover your files. Run `make frontend` first.
- Migrations: `make generate-ini-sqlite` once, then `PROJECT_CONF=tests/sqlite.ini go test ./models/forgejo_migrations/...`. Do not run it at the same time as the integration suite.

Conventions:

- Database migrations go in `models/forgejo_migrations/v18<letter>_<description>.go`. Pick the next free letter (`ls models/forgejo_migrations`).
- New interface strings go in `options/locale_next/locale_en-US.json`.
- After changing the API, run `make generate-swagger`. Do not edit `templates/swagger/v1_json.tmpl` by hand.
- Add tests for what you change, and do not remove existing tests or checks.
- Stage files by name; `make lint` can rewrite `package-lock.json`.

## Licence of contributions and sign-off

Contributions are accepted under the same licence as the project (inbound = outbound): GPL-3.0-or-later. Keep the existing copyright and SPDX headers of the original authors.

Every commit must carry a `Signed-off-by:` line, which certifies the [Developer Certificate of Origin](https://developercertificate.org/). Add it with `git commit -s`.

## Use of AI coding assistants

Much of spoved is written with AI coding agents and reviewed by people. Contributions written with AI assistance are welcome under the same rules as any other: you are responsible for what you submit, you must be able to explain it, and it must carry your sign-off. Please say in the pull request if a substantial part was generated.
