package orchestrator

const FormatDocs = `## Structured Output (optional)

To schedule a follow-up task:
<!-- wt:schedule delay=10m -->Check build status<!-- /wt:schedule -->

To write to memory (requires skill permission):
<!-- wt:memory file="notes" -->Content to save<!-- /wt:memory -->
`
