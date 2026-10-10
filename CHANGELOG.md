# Changelog

## Unreleased

- Agent runs now have no deadline by default. Omitted or zero `timeout_seconds`
  runs until completion, explicit stop, or provider/egg host exit. Positive
  values must be at least 10 seconds, with no upper cap; the former 15-minute
  default and two-hour maximum are removed.
- Run status reports `deadline: "no deadline"` for unbounded runs. Explicit stop,
  process-group cleanup, and recovery after client disconnection or wing restart
  work with or without a deadline. Persistent agent and terminal starts retain
  their existing unlimited lifetime, and wait-tool timeouts remain unchanged.
