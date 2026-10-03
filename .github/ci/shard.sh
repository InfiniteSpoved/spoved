#!/usr/bin/env bash
# Prints the lines of stdin that belong to shard INDEX of TOTAL (1-based),
# distributing the sorted, de-duplicated lines round-robin so that every
# line lands in exactly one shard.
#
# Usage: shard.sh INDEX TOTAL < list
set -euo pipefail

index=${1:?shard index (1-based) required}
total=${2:?number of shards required}
if ! [[ $index =~ ^[0-9]+$ && $total =~ ^[0-9]+$ ]] || ((index < 1 || index > total)); then
  echo "shard.sh: invalid shard $index of $total" >&2
  exit 2
fi

grep -v '^[[:space:]]*$' | LC_ALL=C sort -u | awk -v i="$index" -v n="$total" '(NR - 1) % n == i - 1'
