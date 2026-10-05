#!/usr/bin/env bash
# Code map for agents (#202): graphify, pinned, code-only, offline.
#   tools/codemap.sh            build or refresh graphify-out/
#   tools/codemap.sh query "Q"  query the graph
#   tools/codemap.sh update     refresh after code changes
# Bumping PIN needs a fresh security review.
set -euo pipefail

PIN=35adf432b9d50f6f3d530ab5d7ec316819ef081c
CACHE=${CODEMAP_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/githat-codemap}/graphify

case "${1:-}" in
'') args=(extract . --code-only --no-viz --no-label) ;;
query) [ $# -eq 2 ] || { echo "usage: $0 query \"<question>\"" >&2; exit 1; }; args=(query "$2") ;;
update) [ $# -eq 1 ] || exit 1; args=(update .) ;;
*) echo "usage: $0 [query \"<question>\" | update]" >&2; exit 1 ;;
esac

command -v uv >/dev/null || { echo "codemap: uv not found; install it with your package manager (not auto-installed)" >&2; exit 1; }

if [ ! -d "$CACHE/.git" ]; then
	mkdir -p "$CACHE"
	git -C "$CACHE" init -q
	git -C "$CACHE" remote add origin https://github.com/Graphify-Labs/graphify
fi
if [ "$(git -C "$CACHE" rev-parse -q --verify HEAD || true)" != "$PIN" ]; then
	git -C "$CACHE" fetch -q --depth 1 origin "$PIN"
	git -C "$CACHE" checkout -q --detach "$PIN"
fi
[ "$(git -C "$CACHE" rev-parse HEAD)" = "$PIN" ] && [ -z "$(git -C "$CACHE" status --porcelain -- graphify pyproject.toml uv.lock)" ] ||
	{ echo "codemap: cache $CACHE is not the pinned, clean graphify $PIN; delete it and re-run" >&2; exit 1; }

(cd "$CACHE" && UV_PYTHON_DOWNLOADS=never uv sync --frozen --no-dev --no-install-project --no-build --no-config) ||
	{ echo "codemap: uv sync failed (a system python satisfying graphify's requires-python is needed; no downloads)" >&2; exit 1; }

cd "$(dirname "$0")/.."
env -i PATH="$PATH" HOME="$HOME" LANG=C.UTF-8 PYTHONPATH="$CACHE" GRAPHIFY_NO_AUTO_REFRESH=1 \
	"$CACHE/.venv/bin/python" -m graphify "${args[@]}"
