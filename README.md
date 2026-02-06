# wingthing

A Go harness that orchestrates LLM agents on your behalf.

Wingthing boots CLI agents (`claude -p`, `gemini`, etc.), injects context-rich prompts, manages memory, and coordinates work across machines — so you don't have to type or provide context yourself.

## Concepts

**Poker** — Wingthing "pokes" LLM brains back into itself. It constructs fat prompts with your context, memory, and instructions, then fires them at LLM CLIs. Bidirectional: it reads their output and feeds it forward.

**Memory** — Text-based, human-readable, git-diffable persistence. Wingthing knows about you, your systems, your projects. It carries this context into every agent interaction.

**Orchestration** — Install agents, manage services and crons across OSes, coordinate multi-agent workflows. The toolbox that makes agents useful without you babysitting them.

## Status

Early. Private. Designing the protocol.
