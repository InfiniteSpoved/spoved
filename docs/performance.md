# Git performance under load

How spoved's git over HTTP holds up with 16 to 3,000 users, compared with the Forgejo commit spoved forked from.
Measured on 2026-10-23.

## Result in short

- With default settings, spoved handles 2× the pushes and 2.3× the pulls of Forgejo.
- With 3,000 users working at a normal pace, the slowest 10% of spoved's pushes take 0.29 s, where Forgejo's take 31 s.
- spoved's defaults also beat Forgejo with hand-tuned settings.
- The limit is CPU in the application (git and its hook processes), never the database.

## How it was tested

| | |
|---|---|
| Hardware | One machine with 4 CPU cores and 15 GB RAM, PostgreSQL 16 on the same machine, and the load generator on the same 4 cores. Absolute numbers are small; the ratios carry over to bigger hardware. |
| Forgejo | Upstream `3a81b293` (2026-10-03), the commit spoved forked from. |
| spoved | `develop` at `0a4f1e05` (2026-10-22). |
| Data | 300 private repositories (306 commits, 2.7 MB each), 3,000 users with access tokens, 10 users per repository. |
| Pull | A protocol v2 `git fetch` (`ls-refs`, then `fetch` with wants and haves), and a share of full clones. |
| Push | A new commit on the user's own branch through `git-receive-pack`, with every hook running. |
| Realistic | Each user pauses 0 to 5 minutes between operations, and 20% of operations are pushes. |
| Nonstop | No pauses: deliberate overload. |

Both servers ran back to back on the same machine, each with a fresh database and the same steps in the same order. SSH was not tested; it runs the same hooks plus one more process per operation.

## Default settings

| Step | Forgejo | spoved | Change |
|---|---|---|---|
| Pushes only, 16 users | 7.5 pushes/s, median 2.13 s | 15.4 pushes/s, median 1.03 s | +105% |
| Pulls only, 16 users | 77.8 ops/s, fetch median 0.18 s | 182.7 ops/s, fetch median 0.06 s | +135% |
| Mixed, 100 users | 15.4 ops/s, push median 8.7 s | 28.8 ops/s, push median 3.8 s | +87% |
| Nonstop, 3,000 users | 17.8 ops/s, 0 refused, medians 114 to 147 s | 27.1 ops/s, 1,517 refused as busy, medians 61 to 108 s | +52% completed |
| Realistic, 3,000 users: fetch median / 90th percentile | 0.14 s / 38.8 s | 0.02 s / 0.04 s | |
| Realistic, 3,000 users: push median / 90th percentile | 0.92 s / 31.0 s | 0.23 s / 0.29 s | |

In the realistic run, throughput (18 to 21 operations per second on both) is set by how often users act, so latency is what to compare.

## Tuned settings

Tuned means `[database] MAX_OPEN_CONNS = 100`, `MAX_IDLE_CONNS = 100` and `[security] SUCCESSFUL_TOKENS_CACHE_SIZE = 10000`. spoved now ships the last two as defaults.

| Step | Forgejo | spoved |
|---|---|---|
| Pushes only, 16 users | 8.6 pushes/s | 15.5 pushes/s (+80%) |
| Pulls only, 16 users | 148.4 ops/s | 171.5 ops/s (+16%) |
| Nonstop, 3,000 users | 19.4 ops/s, 0 refused | 24.7 ops/s, 1,954 refused as busy |
| Realistic, 3,000 users: fetch median / 99th percentile | 0.04 s / 4.3 s | 0.02 s / 0.07 s |
| Realistic, 3,000 users: push median / 99th percentile | 0.45 s / 5.0 s | 0.24 s / 0.35 s |

## What made the difference

| Finding | Change | Effect |
|---|---|---|
| The access-token cache was never created (a one-character bug inherited from upstream), so every git request re-hashed its token with PBKDF2. With only 2 idle database connections, PostgreSQL also opened about 70 new connections per second. | [#567](https://github.com/InfiniteSpoved/spoved/pull/567) fixes the cache, keeps idle connections open and skips the `update` hook | Pulls 52 → 146 ops/s, pushes 6.1 → 8.2/s |
| Each push starts the spoved binary as git hooks, and about 40% of that start-up was garbage collection. | [#575](https://github.com/InfiniteSpoved/spoved/pull/575) runs the hooks with garbage collection off | Pushes 8.2 → 11.6/s |
| Under heavy git load, web pages slowed from 0.05 s to 1.7 s, because git work took every core. | [#578](https://github.com/InfiniteSpoved/spoved/pull/578) caps concurrent git operations | Web median 1.7 → 0.32 s at 100 users |
| A repository created by an import has no reachability bitmap, so every full clone walks its whole history (4.9 s of CPU for a repository the size of spoved). | [#584](https://github.com/InfiniteSpoved/spoved/pull/584) writes a bitmap at import | Clones of an imported repository 0.6 → 1.7/s |

The bitmap gain doesn't show in the tables above, because the test repositories are small and were created by push, not imported.

## When the server is full

Under nonstop overload, Forgejo keeps every request waiting, for two minutes or more at 3,000 users. spoved refuses a git operation it can't start within a minute with HTTP 503 and "The server is busy, please try again in a moment.", so web pages stay responsive. git does not retry by itself. The limit and the wait are `[git] MAX_CONCURRENT_OPERATIONS` (default 4 per CPU core, 0 = no limit) and `OPERATION_QUEUE_TIMEOUT` (default 1 minute).

The audit log costs spoved 3 to 5% more CPU per operation than Forgejo, which the changes above more than make up for.

## Still open

- [#568](https://github.com/InfiniteSpoved/spoved/issues/568): a smaller hook binary, which would make pushes cheaper still.
- [#569](https://github.com/InfiniteSpoved/spoved/issues/569): per-user git rate limits, so one user can't fill the cap.
- [#570](https://github.com/InfiniteSpoved/spoved/issues/570): repacking after pushes, and a cache for identical fetches.
