#!/usr/bin/env bash
# Verify ADR hygiene:
#   1. Every ADR file is indexed in README.md.
#   2. Every README link points at a file that exists.
#   3. Every ADR file has a Status line under "## Status" with a valid value.
#   4. Every ADR file has the four standard sections.
#
# Mirrors the bre-go pattern; the markup-svc inherits the same discipline.
set -euo pipefail

ADR_DIR="docs/architecture/decisions"
INDEX="$ADR_DIR/README.md"

if [ ! -f "$INDEX" ]; then
  echo "missing $INDEX" >&2
  exit 1
fi

fail=0

for f in "$ADR_DIR"/0*.md; do
  base="$(basename "$f")"
  if ! grep -q "($base)" "$INDEX"; then
    echo "ADR file not indexed in README.md: $base" >&2
    fail=1
  fi

  status="$(awk '/^## Status$/{flag=1; next} flag && NF{print; exit}' "$f")"
  if [ -z "$status" ]; then
    echo "ADR has no Status line: $base" >&2
    fail=1
    continue
  fi
  case "$status" in
    "Proposed"*|"Accepted"|"Accepted "*|"Superseded by ADR-"*|"Deprecated") ;;
    *)
      echo "ADR $base has unknown status: \"$status\"" >&2
      echo "  allowed: Proposed | Accepted | Accepted (...) | Superseded by ADR-NNNN | Deprecated" >&2
      fail=1
      ;;
  esac

  for section in "## Status" "## Context" "## Decision" "## Consequences"; do
    if ! grep -qFx "$section" "$f"; then
      echo "ADR $base is missing the section: $section" >&2
      fail=1
    fi
  done
done

while IFS= read -r linked; do
  if [ ! -f "$ADR_DIR/$linked" ]; then
    echo "README.md links to missing ADR file: $linked" >&2
    fail=1
  fi
done < <(grep -oE '\(([0-9]{4}-[a-z0-9-]+\.md)\)' "$INDEX" | tr -d '()' | sort -u)

if [ "$fail" -ne 0 ]; then
  exit 1
fi

count=$(find "$ADR_DIR" -maxdepth 1 -name '0*.md' | wc -l | tr -d ' ')
echo "ADR index in sync ($count ADRs, all with valid status)"
