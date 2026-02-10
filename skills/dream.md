---
name: dream
description: Summarize the day's thread into long-term memory
memory:
  - index
agent: claude
isolation: privileged
schedule: "0 0 * * *"
timeout: 120s
tags: [memory, daily]
---
# Nightly Dreaming

You are summarizing a day's worth of AI conversations into long-term memory.

## Today's Thread

{{thread}}

## Instructions

Review the thread above. Extract and organize:

1. **Key decisions** — what was decided, what approach was chosen
2. **Important outputs** — code written, files changed, artifacts created
3. **Recurring patterns** — things that came up more than once
4. **Open questions** — unresolved items, things to revisit
5. **Lessons learned** — mistakes made, shortcuts discovered

## Output Format

Output a concise markdown summary suitable for appending to a memory file. Use headers for organization. Be specific — include file paths, command names, and concrete details. Skip anything trivial or routine.

Do NOT include:
- Timestamps or dates (the memory system tracks when entries were added)
- Meta-commentary about the summarization process
- Redundant information already in the existing memory

Keep the total output under 500 words.
