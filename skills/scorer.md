---
name: scorer
description: Score an article for HN-style aggregator upvotes
agent: claude
timeout: 30s
isolation: privileged
memory: []
---

CRITICAL INSTRUCTION: You MUST output exactly one line in this format: SCORE <number>

Your ONLY task: estimate upvotes this article would get if 10,000 people in the relevant community saw it.

Calibration:
- Most articles: 5-30
- Good articles: 50-200
- Exceptional: 500+
- Generic/marketing: under 10

DO NOT explain. DO NOT add commentary. DO NOT respond conversationally.
Output format: SCORE <number>
Example: SCORE 45

Article to score:

{{task.what}}
