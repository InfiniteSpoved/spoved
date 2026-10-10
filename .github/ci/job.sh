#!/usr/bin/env bash
# The CI jobs, run by the public InfiniteSpoved/spovedBuild workflow, which
# keeps their output out of its public logs (it only uploads it encrypted).
# Keep anything that describes this repository in here, not in that workflow.
#
# Usage: job.sh TASK [ARGS]
#   plan                 writes `tasks`, the jobs to run, to $GITHUB_OUTPUT
#   lint | frontend | migrations
#   unit SHARD SHARDS | integration SHARD SHARDS | e2e SHARD SHARDS
#
# Runs do the checks and the unit tests; with FULL=true they also run the
# migration, integration and end-to-end tests. Pull requests dispatched with
# their commits (EVENT=repository_dispatch) that only change files the Go
# tests cannot depend on skip the unit tests. The unit, integration and end-to-end tests are split into shards that
# take a round-robin share of the packages, tests or test files (see shard.sh).
# Each shard is its own machine, so integration shards share no state.
set -euo pipefail

# Forgejo tests use SQLite unless told otherwise
export TAGS='sqlite sqlite_unlock_notify' TEST_TAGS='sqlite sqlite_unlock_notify'

task=$1
shift

case $task in
plan)
  backend=true
  if [ "$EVENT" = repository_dispatch ]; then
    files=$(git diff --name-only "$BASE_SHA...$HEAD_SHA")
    echo "Changed files:"
    echo "$files"
    # Files that cannot affect the Go unit, migration and integration
    # tests. Anything else, or an empty diff, runs them.
    safe='^(web_src/|tests/e2e/|\.github/ISSUE_TEMPLATE/|(.*/)?[^/]+\.md$|package(-lock)?\.json$|\.npmrc$|tsconfig\.json$|eslint\.config\.mjs$|stylelint\.config\.js$|vitest\.config\.ts$|webpack\.config\.js$|tailwind\.config\.js$|playwright\.config\.ts$)'
    if [ -n "$files" ] && ! grep -qvE "$safe" <<< "$files"; then
      echo "Only frontend, end-to-end or documentation files changed: skipping the Go test suites"
      backend=false
    fi
  fi
  # depth: commits to fetch, 0 for the full history
  tasks='{"task":"lint","args":"","depth":"1"},{"task":"frontend","args":"","depth":"1"}'
  if [ "$backend" = true ]; then
    for i in $(seq 1 16); do tasks+=",{\"task\":\"unit\",\"args\":\"$i 16\",\"depth\":\"1\"}"; done
  fi
  if [ "$FULL" = true ]; then
    # only the selection of changed end-to-end tests needs the history
    depth=1
    [ "$RUN_ALL_E2E" = true ] || depth=0
    tasks+=',{"task":"migrations","args":"","depth":"1"}'
    for i in 1 2 3 4; do tasks+=",{\"task\":\"integration\",\"args\":\"$i 4\",\"depth\":\"1\"}"; done
    for i in 1 2 3; do tasks+=",{\"task\":\"e2e\",\"args\":\"$i 3\",\"depth\":\"$depth\"}"; done
  fi
  tasks="[$tasks]"
  # ONLY (comma-separated task names) keeps just those stages, for iterating
  # on a fix; such runs report "CI partial", never "CI success"
  if [ -n "${ONLY:-}" ]; then
    tasks=$(jq -c --arg only "$ONLY" '[.[] | select(.task as $t | $only | split(",") | index($t))]' <<< "$tasks")
  fi
  echo "tasks=$tasks" | tee -a "$GITHUB_OUTPUT"
  ;;

lint)
  # lint-renovate needs node_modules
  make deps-frontend
  # golangci-lint, go vet, locale, editorconfig, swagger and format checks,
  # then a build; cimake.sh runs the independent targets in parallel
  ./tools/cimake.sh pr-go
  ;;

frontend)
  make deps-frontend
  make lint-frontend
  make checks-frontend
  make test-frontend
  make frontend
  ;;

unit)
  # Azure Blob Storage emulator for the azureblob storage tests (TEST_AZURITE_URL)
  npx -y -p azurite@3.37.0 azurite-blob --inMemoryPersistence --skipApiVersionCheck --blobHost 127.0.0.1 > "$RUNNER_TEMP/azurite.log" 2>&1 &
  timeout 120 bash -c 'until grep -q "successfully listens" "$RUNNER_TEMP/azurite.log"; do sleep 1; done' || { cat "$RUNNER_TEMP/azurite.log"; exit 1; }
  # the packages `make test-backend` tests, as computed by the Makefile
  packages=$(make -s --eval='ci-list-test-packages: compute-go-test-packages ; @echo $(GO_TEST_PACKAGES)' ci-list-test-packages \
    | tr ' ' '\n' | .github/ci/shard.sh 2 4 | paste -sd' ')
  # temporary probe: job N reports whether pattern N appears in one package's output
  set +e
  RACE_ENABLED=true TEST_AZURITE_URL=127.0.0.1:10000 make test-backend GO_TEST_PACKAGES="$packages" > "$RUNNER_TEMP/out.txt" 2>&1
  cat "$RUNNER_TEMP/out.txt"
  set -e
  o="$RUNNER_TEMP/out.txt"
  pats=('no space' '^go: |go: ' 'fatal error' 'goroutine ' '^FAIL' '^ok ' '^\? ' 'services/u' 'services/task' 'exit status' 'nterrupt|Terminated' 'vet' 'link' 'cannot|unable' 'rror' 'Makefile:574')
  ! tail -30 "$o" | grep -qE "${pats[$1 - 1]}"
  ;;

migrations)
  USE_REPO_TEST_DIR=1 make test-sqlite-migration
  ;;

integration)
  # test repositories outside the checkout, like USE_REPO_TEST_DIR
  make integrations.sqlite.test generate-ini-sqlite REPO_TEST_DIR="$(mktemp -d)/"
  # this shard's tests, e.g. ^(TestA|TestB)$; -test.list also prints log lines
  tests=$(PROJECT_ROOT="$PWD" PROJECT_CONF=tests/sqlite.ini ./integrations.sqlite.test -test.list '^Test' 2>/dev/null \
    | grep -E '^Test[A-Za-z0-9_]*$' | .github/ci/shard.sh "$1" "$2" | paste -sd'|')
  [ -n "$tests" ] || { echo "no integration tests in shard $1 of $2" >&2; exit 1; }
  echo "Testing $(tr -cd '|' <<< "$tests" | wc -c | awk '{print $1 + 1}') tests in shard $1 of $2"
  PROJECT_ROOT="$PWD" PROJECT_CONF=tests/sqlite.ini ./integrations.sqlite.test -test.run "^(${tests})\$"
  ;;

e2e)
  # Firefox only, like Forgejo's own CI: its timing-sensitive tests are tuned for it
  browser=firefox
  make deps-frontend
  make frontend
  npx playwright install --with-deps "$browser"
  make e2e.sqlite.test generate-ini-sqlite
  # With RUN_ALL_E2E unchecked, only run the tests whose `@watch` patterns
  # match the files changed compared with develop (see tests/e2e/changes.go).
  # An empty CHANGED_FILES is meaningful: nothing relevant changed, so no test runs.
  if [ "$RUN_ALL_E2E" = true ]; then
    echo "Running all end-to-end tests"
    export RUN_ALL=1
  else
    echo "Files changed compared with develop:"
    git diff --name-only origin/develop...HEAD
    # tests/e2e/changes.go splits the list on a literal "\n"
    CHANGED_FILES=$(git diff --name-only origin/develop...HEAD | awk 'NR > 1 {printf "\\n"} {printf "%s", $0}')
    export CHANGED_FILES
  fi
  # this shard's test files, e.g. ^TestE2e$/^(login\.test\.e2e|...)$
  files=$(find tests/e2e -maxdepth 1 -name '*.test.e2e.ts' -printf '%f\n' | sed 's/\.ts$//' \
    | .github/ci/shard.sh "$1" "$2" | sed 's/\./\\./g' | paste -sd'|')
  DISPLAY='' USE_REPO_TEST_DIR=1 PLAYWRIGHT_PROJECT=$browser PROJECT_ROOT="$PWD" PROJECT_CONF=tests/sqlite.ini \
    ./e2e.sqlite.test -test.run "^TestE2e\$/^(${files})\$"
  ;;

*)
  echo "job.sh: unknown task $task" >&2
  exit 2
  ;;
esac
