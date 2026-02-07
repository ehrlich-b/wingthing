#!/bin/bash
set -euo pipefail

# Generate spaces/{slug}/description.txt and centroid.txt using claude -p (sonnet).
#
# Usage:
#   ./scripts/gen_spaces.sh physics          # generate one space
#   ./scripts/gen_spaces.sh physics compilers # generate specific spaces
#   ./scripts/gen_spaces.sh                   # generate all from spaces.csv
#
# POST-RUN: After bulk generation, run trim_centroids to fix oversized ones:
#   ./scripts/trim_centroids.sh

REPO="$(cd "$(dirname "$0")/.." && pwd)"
SPACES_DIR="$REPO/spaces"
MODEL="claude-sonnet-4-5-20250929"

gen_space() {
  local slug="$1"
  local dir="$SPACES_DIR/$slug"
  mkdir -p "$dir"

  # Description
  if [ ! -f "$dir/description.txt" ]; then
    echo "  [$slug] description..."
    claude -p --model "$MODEL" "Write a 5-10 word description for a topic community called '$slug'. This appears on the about page. Be specific and cerebral, not generic. Output ONLY the description, nothing else. No quotes." > "$dir/description.txt"
    echo "    -> $(cat "$dir/description.txt")"
  else
    echo "  [$slug] description exists: $(cat "$dir/description.txt")"
  fi

  # Centroid
  if [ ! -f "$dir/centroid.txt" ]; then
    echo "  [$slug] centroid..."
    claude -p --model "$MODEL" "Generate a semantic centroid for the topic '$slug'.

A centroid is a dense keyword cloud for embedding similarity matching. NOT prose. A bag of the most relevant technical terms, concepts, names, and jargon.

HARD LIMIT: 400-550 characters. Count carefully. Stop at 512.

Rules:
- Dense keywords separated by spaces
- Include key concepts, important names/tools, technical jargon, subtopics
- Target the BEST content — posts that would get 200 upvotes on Hacker News
- No filler words, no articles, no sentences
- The precision of these keywords IS the quality filter

Output ONLY the centroid. No quotes, no preamble, no explanation. Stay under 550 characters." > "$dir/centroid.txt"
    local chars=$(wc -c < "$dir/centroid.txt")
    echo "    -> ${chars} chars"
  else
    local chars=$(wc -c < "$dir/centroid.txt")
    echo "  [$slug] centroid exists: ${chars} chars"
  fi
}

if [ $# -gt 0 ]; then
  # Generate specific slugs
  for slug in "$@"; do
    gen_space "$slug"
  done
else
  # Generate all from spaces.csv
  total=$(grep -c . "$REPO/spaces.csv")
  i=0
  while IFS= read -r slug; do
    [ -z "$slug" ] && continue
    i=$((i + 1))
    echo "[$i/$total]"
    gen_space "$slug"
  done < "$REPO/spaces.csv"
fi
