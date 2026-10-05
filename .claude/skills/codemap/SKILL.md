---
name: codemap
description: Query a local code graph of this repo before grepping or reading files. Use at the start of any task that needs finding where code lives.
---

# Codemap

1. Once per session: `tools/codemap.sh` (builds `graphify-out/`, git-ignored; offline, code-only).
2. Before grepping or reading files: `tools/codemap.sh query "<question>"`. Query with symbol, function or type names, not natural language; fall back to grep if the result looks off.
3. After your code changes: `tools/codemap.sh update`.

Only these three commands. Never call graphify directly, and never run its `install`, `claude install`, `hook install`, `add`, `ingest` or `--transport` commands. They rewrite CLAUDE.md and settings and add git hooks.
