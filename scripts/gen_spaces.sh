#!/bin/bash
set -euo pipefail

# Generate spaces/{slug}/description.txt and centroid.txt using claude -p (sonnet).
# Reads slugs from spaces.csv, one per line. Idempotent — skips existing files.

REPO="$(cd "$(dirname "$0")/.." && pwd)"
SPACES_DIR="$REPO/spaces"
SLUGS_FILE="$REPO/spaces.csv"
MODEL="claude-sonnet-4-5-20250929"

mkdir -p "$SPACES_DIR"

total=$(grep -c . "$SLUGS_FILE")
i=0

while IFS= read -r slug; do
  [ -z "$slug" ] && continue
  i=$((i + 1))

  dir="$SPACES_DIR/$slug"
  mkdir -p "$dir"

  # Generate description (5-10 words) — skip if exists
  if [ ! -f "$dir/description.txt" ]; then
    echo "[$i/$total] $slug — description"
    claude -p --model "$MODEL" "Write a 5-10 word description for a topic community called '$slug'. This appears on the about page. Be specific and cerebral, not generic. Output ONLY the description, nothing else. No quotes." > "$dir/description.txt"
  fi

  # Generate centroid (~512 chars keyword cloud) — skip if exists
  if [ ! -f "$dir/centroid.txt" ]; then
    echo "[$i/$total] $slug — centroid"
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
  else
    echo "[$i/$total] $slug — already done"
  fi

done < "$SLUGS_FILE"

echo "=== Done: $i spaces processed in $SPACES_DIR ==="
