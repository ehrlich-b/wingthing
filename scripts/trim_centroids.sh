#!/bin/bash
set -euo pipefail

# Find centroids > 700 chars and trim them down using claude -p (opus).
# Run this AFTER gen_spaces.sh bulk generation.

REPO="$(cd "$(dirname "$0")/.." && pwd)"
SPACES_DIR="$REPO/spaces"
MODEL="claude-opus-4-6"

found=0
for f in "$SPACES_DIR"/*/centroid.txt; do
  chars=$(wc -c < "$f" | tr -d ' ')
  slug=$(basename "$(dirname "$f")")
  if [ "$chars" -gt 700 ]; then
    found=$((found + 1))
    echo "[$slug] ${chars} chars — trimming..."
    original=$(cat "$f")
    claude -p --model "$MODEL" "Trim this keyword centroid to under 550 characters. Keep the most important terms, cut redundancy. Output ONLY the trimmed centroid, nothing else.

$original" > "$f"
    new_chars=$(wc -c < "$f" | tr -d ' ')
    echo "  -> ${new_chars} chars"
  fi
done

if [ "$found" -eq 0 ]; then
  echo "All centroids under 700 chars. Nothing to trim."
fi
