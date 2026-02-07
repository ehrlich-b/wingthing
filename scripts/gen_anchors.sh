#!/bin/bash
set -euo pipefail

# Generate anchors.yaml using claude -p (sonnet) for centerpoint generation.
# Step 1: Generate the slug list (128 cerebral subreddits)
# Step 2: Loop through each, generate ~512 char keyword centerpoint

OUT="$(dirname "$0")/../anchors.yaml"
SLUGS_FILE="/tmp/wt_anchor_slugs.txt"
MODEL="claude-sonnet-4-5-20250929"

echo "=== Step 1: Generate 128 anchor slugs ==="

claude -p --model "$MODEL" "You are generating a list of 128 topic slugs for a semantic link aggregator.

The audience is the kind of person curious enough to figure out what an embedding-based social platform is. Think Hacker News, not Reddit mainstream. Cerebral, technical, intellectually curious.

Output EXACTLY 128 lines, one per line, in this format:
slug|Short 5-10 word description

Rules:
- slug is lowercase, hyphenated, URL-safe (e.g., distributed-systems, philosophy-of-mind)
- Description is human-readable, for an about page
- No duplicates, no overlaps (don't have both 'ai' and 'artificial-intelligence')
- Cover: hard science, engineering, programming languages, systems, math, security, finance, philosophy, economics, history, linguistics, design, writing, music, art, open source, privacy, health, climate, space, games, and niche technical topics
- Bias toward depth over breadth. 'type-theory' over 'computers'. 'mechanism-design' over 'business'.
- Include some delightfully specific ones: cellular-automata, ham-radio, conlangs, demoscene
- No lifestyle fluff: no fitness, cooking, travel, parenting, fashion
- This is for nerds. Lean into it.

Output ONLY the 128 lines. No preamble, no numbering, no commentary." > "$SLUGS_FILE"

COUNT=$(wc -l < "$SLUGS_FILE" | tr -d ' ')
echo "Got $COUNT slugs"

echo "=== Step 2: Generate centerpoints ==="

# Start the YAML file
echo "anchors:" > "$OUT"

while IFS='|' read -r slug desc; do
  # Skip empty lines
  [ -z "$slug" ] && continue

  echo "  [$slug] generating centerpoint..."

  centerpoint=$(claude -p --model "$MODEL" "Generate a ~512 character semantic centerpoint for the topic: $slug ($desc)

A centerpoint is a dense keyword cloud optimized for embedding similarity matching. It is NOT prose. It is a bag of the most relevant technical terms, concepts, names, and jargon that content about this topic would contain.

Rules:
- ~512 characters (400-600 ok)
- Dense keywords separated by spaces, no sentences
- Include: key concepts, important names/tools, technical jargon, subtopics
- The embedding model will use this to match incoming posts to this topic
- Think: what words would appear in the BEST posts about this topic?
- No filler words, no articles, no verbs unless they're domain terms
- No 'no spam' or negative keywords — embeddings don't understand negation

Example for 'compilers':
Compiler design parsing lexing tokenizer abstract syntax tree AST type checking type inference Hindley-Milner type system code generation LLVM IR intermediate representation SSA static single assignment register allocation instruction selection optimization passes dead code elimination constant folding loop unrolling inlining JIT just-in-time compilation interpreters bytecode virtual machine garbage collection memory management language design syntax semantics grammars context-free grammar PEG recursive descent parser generator yacc bison self-hosting bootstrapping

Output ONLY the centerpoint text. No quotes, no preamble, no explanation.")

  # Write YAML entry
  cat >> "$OUT" << YAMLEOF
  - slug: $slug
    label: $(echo "$desc" | sed 's/^ *//')
    description: $(echo "$desc" | sed 's/^ *//')
    centerpoint: >
      $centerpoint
YAMLEOF

done < "$SLUGS_FILE"

FINAL_COUNT=$(grep -c "^  - slug:" "$OUT")
echo "=== Done: $FINAL_COUNT anchors written to $OUT ==="
