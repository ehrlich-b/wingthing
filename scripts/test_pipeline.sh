#!/bin/bash
set -euo pipefail

# End-to-end pipeline test: RSS posts → embed → assign to spaces.
# Validates that real content lands in the right spaces.

REPO="$(cd "$(dirname "$0")/.." && pwd)"
SPACES_DIR="$REPO/spaces"
TEST_DIR="/tmp/wt_pipeline_test"

mkdir -p "$TEST_DIR"

if [ -z "${OPENAI_API_KEY:-}" ]; then
  echo "Error: OPENAI_API_KEY not set"
  exit 1
fi

export SPACES_DIR TEST_DIR

python3 <<'PYEOF'
import json, struct, os, urllib.request, math, xml.etree.ElementTree as ET

spaces_dir = os.environ["SPACES_DIR"]
test_dir = os.environ["TEST_DIR"]
api_key = os.environ["OPENAI_API_KEY"]

# --- Step 1: Fetch posts from RSS ---
print("=== Step 1: Fetching posts from RSS ===")

feeds = [
    ("https://hnrss.org/newest?count=8", "hn"),
    ("https://lobste.rs/rss", "lobsters"),
    ("https://jvns.ca/atom.xml", "jvns"),
]

posts = []
for url, source in feeds:
    print(f"  {source}...")
    try:
        req = urllib.request.Request(url, headers={"User-Agent": "wingthing-test/1.0"})
        with urllib.request.urlopen(req, timeout=10) as resp:
            data = resp.read().decode()
        root = ET.fromstring(data)
        ns = {"atom": "http://www.w3.org/2005/Atom"}

        entries = root.findall(".//atom:entry", ns)
        if entries:
            for entry in entries[:5]:
                title = (entry.findtext("atom:title", "", ns) or "").strip()
                summary = (entry.findtext("atom:summary", "", ns) or "").strip()[:500]
                if title:
                    posts.append({"title": title, "summary": summary, "source": source})
        else:
            for item in root.findall(".//item")[:5]:
                title = (item.findtext("title") or "").strip()
                desc = (item.findtext("description") or "").strip()[:500]
                if title:
                    posts.append({"title": title, "summary": desc, "source": source})
    except Exception as e:
        print(f"    Error: {e}")

print(f"  Got {len(posts)} posts")

# --- Step 2: Build texts for embedding ---
texts = []
for p in posts:
    text = p["title"]
    if p["summary"]:
        text += ". " + p["summary"]
    texts.append(text[:1024])

# --- Step 3: Load space embeddings ---
print("\n=== Step 2: Loading space embeddings ===")
spaces = {}
for slug in sorted(os.listdir(spaces_dir)):
    emb_path = os.path.join(spaces_dir, slug, "embedding.bin")
    if os.path.exists(emb_path):
        with open(emb_path, "rb") as f:
            data = f.read()
        spaces[slug] = list(struct.unpack(f"{len(data)//4}f", data))
print(f"  {len(spaces)} spaces loaded: {', '.join(sorted(spaces.keys()))}")

# --- Step 4: Embed posts ---
print(f"\n=== Step 3: Embedding {len(texts)} posts ===")
req = urllib.request.Request(
    "https://api.openai.com/v1/embeddings",
    data=json.dumps({"model": "text-embedding-3-small", "input": texts, "dimensions": 512}).encode(),
    headers={"Content-Type": "application/json", "Authorization": f"Bearer {api_key}"},
)
with urllib.request.urlopen(req, timeout=30) as resp:
    result = json.loads(resp.read())

post_embs = [None] * len(texts)
for item in result["data"]:
    post_embs[item["index"]] = item["embedding"]

# --- Step 5: Assign ---
def cosine(a, b):
    dot = sum(x*y for x,y in zip(a,b))
    na = math.sqrt(sum(x*x for x in a))
    nb = math.sqrt(sum(x*x for x in b))
    return dot / (na * nb) if na and nb else 0.0

print(f"\n{'='*95}")
print(f"{'':3} {'Post':<50} {'Top Space':>14} {'Sim':>6}  {'#2':>14} {'Sim':>6}")
print(f"{'='*95}")

assigned = frontier = swallowed = 0

for i, post in enumerate(posts):
    sims = [(slug, cosine(post_embs[i], vec)) for slug, vec in spaces.items()]
    sims.sort(key=lambda x: -x[1])
    s1, sim1 = sims[0]
    s2, sim2 = sims[1] if len(sims) > 1 else ("", 0.0)
    title = post["title"][:48]

    if sim1 >= 0.40:
        m = ">>>"
        assigned += 1
    elif sim1 >= 0.25:
        m = " ~ "
        frontier += 1
    else:
        m = " X "
        swallowed += 1

    print(f"{m} {title:<48} {s1:>14} {sim1:.3f}  {s2:>14} {sim2:.3f}")

print(f"{'='*95}")
print(f"Assigned (>=0.40): {assigned}  |  Frontier (0.25-0.40): {frontier}  |  Swallowed (<0.25): {swallowed}")
print(f"Total: {len(posts)} posts against {len(spaces)} spaces")
PYEOF
