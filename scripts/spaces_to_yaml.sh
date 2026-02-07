#!/bin/bash
set -euo pipefail

# Convert spaces/{slug}/ directories into a single spaces.yaml file.
# Reads description.txt and centroid.txt from each directory.
# Skips spaces with missing or empty centroids.

REPO="$(cd "$(dirname "$0")/.." && pwd)"
SPACES_DIR="$REPO/spaces"
OUT="$REPO/spaces.yaml"

if [ ! -d "$SPACES_DIR" ]; then
  echo "No spaces/ directory found" >&2
  exit 1
fi

: > "$OUT"

count=0
skipped=0

for dir in "$SPACES_DIR"/*/; do
  slug="$(basename "$dir")"
  desc_file="$dir/description.txt"
  centroid_file="$dir/centroid.txt"

  # Must have both files
  if [ ! -f "$desc_file" ] || [ ! -f "$centroid_file" ]; then
    echo "  skip $slug (missing files)"
    skipped=$((skipped + 1))
    continue
  fi

  desc="$(cat "$desc_file" | tr -d '\n')"
  centroid="$(cat "$centroid_file" | tr -d '\n')"

  # Skip empty centroids
  if [ -z "$centroid" ]; then
    echo "  skip $slug (empty centroid)"
    skipped=$((skipped + 1))
    continue
  fi

  # Escape any literal backslashes or problematic chars in YAML
  cat >> "$OUT" <<EOF
- slug: $slug
  description: $desc
  centroid: >-
    $centroid

EOF
  count=$((count + 1))
done

echo "Wrote $count spaces to $OUT ($skipped skipped)"
