#!/bin/bash
set -euo pipefail

# Generate embedding.bin for each space using OpenAI text-embedding-3-small (512 dims).
# Reads centroid.txt, calls API, writes raw float32 blob. Idempotent.
#
# Usage:
#   ./scripts/gen_embeddings.sh              # all spaces
#   ./scripts/gen_embeddings.sh physics ml   # specific spaces

REPO="$(cd "$(dirname "$0")/.." && pwd)"
SPACES_DIR="$REPO/spaces"

if [ -z "${OPENAI_API_KEY:-}" ]; then
  echo "Error: OPENAI_API_KEY not set"
  exit 1
fi

# Collect spaces needing embeddings
todo=()
if [ $# -gt 0 ]; then
  todo=("$@")
else
  for dir in "$SPACES_DIR"/*/; do
    slug=$(basename "$dir")
    centroid="$dir/centroid.txt"
    if [ -f "$centroid" ] && [ -s "$centroid" ] && [ ! -f "$dir/embedding.bin" ]; then
      todo+=("$slug")
    fi
  done
fi

if [ ${#todo[@]} -eq 0 ]; then
  echo "All embeddings up to date."
  exit 0
fi

echo "Generating embeddings for ${#todo[@]} spaces..."

# Process in batches of 20
batch_size=20
for ((start=0; start<${#todo[@]}; start+=batch_size)); do
  batch=("${todo[@]:$start:$batch_size}")

  SPACES_DIR="$SPACES_DIR" python3 - "${batch[@]}" <<'PYEOF'
import sys, json, struct, urllib.request, os

spaces_dir = os.environ["SPACES_DIR"]
slugs = sys.argv[1:]
api_key = os.environ["OPENAI_API_KEY"]

# Read centroids, skip empty
texts = []
valid_slugs = []
for slug in slugs:
    path = f"{spaces_dir}/{slug}/centroid.txt"
    if not os.path.exists(path):
        print(f"    {slug} — no centroid.txt, skipping")
        continue
    with open(path) as f:
        text = f.read().strip()
    if not text:
        print(f"    {slug} — empty centroid, skipping")
        continue
    texts.append(text)
    valid_slugs.append(slug)

if not texts:
    sys.exit(0)

print(f"  Batch: {' '.join(valid_slugs)}")

req = urllib.request.Request(
    "https://api.openai.com/v1/embeddings",
    data=json.dumps({"model": "text-embedding-3-small", "input": texts, "dimensions": 512}).encode(),
    headers={"Content-Type": "application/json", "Authorization": f"Bearer {api_key}"},
)
try:
    with urllib.request.urlopen(req, timeout=30) as resp:
        result = json.loads(resp.read())
except urllib.error.HTTPError as e:
    body = e.read().decode()
    print(f"  API error {e.code}: {body}")
    sys.exit(1)

for item in sorted(result["data"], key=lambda x: x["index"]):
    slug = valid_slugs[item["index"]]
    emb = item["embedding"]
    with open(f"{spaces_dir}/{slug}/embedding.bin", "wb") as f:
        f.write(struct.pack(f"{len(emb)}f", *emb))
    print(f"    {slug} — {len(emb)*4} bytes")
PYEOF

done

echo "=== Done: ${#todo[@]} embeddings generated ==="
