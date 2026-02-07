#!/bin/bash
set -euo pipefail

# Generate embedding.bin for each space using OpenAI text-embedding-3-small (512 dims).
# Reads centroid.txt, calls API, writes raw float32 blob. Idempotent.

REPO="$(cd "$(dirname "$0")/.." && pwd)"
SPACES_DIR="$REPO/spaces"

if [ -z "${OPENAI_API_KEY:-}" ]; then
  echo "Error: OPENAI_API_KEY not set"
  exit 1
fi

# Collect spaces needing embeddings
todo=()
for dir in "$SPACES_DIR"/*/; do
  slug=$(basename "$dir")
  if [ -f "$dir/centroid.txt" ] && [ ! -f "$dir/embedding.bin" ]; then
    todo+=("$slug")
  fi
done

if [ ${#todo[@]} -eq 0 ]; then
  echo "All embeddings up to date."
  exit 0
fi

echo "Generating embeddings for ${#todo[@]} spaces..."

# Process in batches of 20
batch_size=20
for ((start=0; start<${#todo[@]}; start+=batch_size)); do
  batch=("${todo[@]:$start:$batch_size}")

  # Build JSON with python (handles escaping properly)
  python3 - "${batch[@]}" <<'PYEOF'
import sys, json, struct, urllib.request, os

spaces_dir = os.environ.get("SPACES_DIR", "spaces")
slugs = sys.argv[1:]
api_key = os.environ["OPENAI_API_KEY"]

# Read centroids
texts = []
for slug in slugs:
    with open(f"{spaces_dir}/{slug}/centroid.txt") as f:
        texts.append(f.read().strip())

print(f"  Batch: {' '.join(slugs)}")

# Call OpenAI
req = urllib.request.Request(
    "https://api.openai.com/v1/embeddings",
    data=json.dumps({"model": "text-embedding-3-small", "input": texts, "dimensions": 512}).encode(),
    headers={"Content-Type": "application/json", "Authorization": f"Bearer {api_key}"},
)
with urllib.request.urlopen(req, timeout=30) as resp:
    result = json.loads(resp.read())

# Sort by index, write binary
for item in sorted(result["data"], key=lambda x: x["index"]):
    slug = slugs[item["index"]]
    emb = item["embedding"]
    with open(f"{spaces_dir}/{slug}/embedding.bin", "wb") as f:
        f.write(struct.pack(f"{len(emb)}f", *emb))
    print(f"    {slug} — {len(emb)*4} bytes")
PYEOF

done

echo "=== Done: ${#todo[@]} embeddings generated ==="
