# Browser reader fixtures

`codex-native.json` is synthetic and contains no account, provider process or model output. Its wire records and normalized event fields follow these pinned source contracts at `014b7e5`:

- [Native prompt, text and MCP tool items](../../../internal/egg/codex_native_test.go), `TestCodexNativePromptAndToolEvidence` and `TestCodexNativeAssistantItemCannotCompleteTurn`.
- [Native item normalization and completion](../../../internal/egg/codex_native.go), `ParseCodexNativeEvent`.
- [Reconnect and unsupported app-server readiness](../../../test/integ/codex_native_journal_test.go), `TestCodexNativeJournalReconnectWithoutClaimingAdapterSupport`.
- [Hook-backed idle/completion and unknown MCP activity](../../../internal/egg/lifecycle.go), `importProviderHooks` and `ReadSessionLifecycle`.
- [Prompt readiness and receipt replay](../../../internal/egg/session_prompt.go), `NativePromptReady` and `SubmitSessionPrompt`.

The final item is deliberately unfamiliar. The browser retains its full JSON. The native protocol helper accepts unknown item types as provider evidence; this does not enable the app-server transport or its input controls. Only fresh hook-backed idle/completed state with an exact provider identity enables Codex input.

`codex_browser_fixture_test.go` checks every native fixture against the Go parser, including type, role, text and state. Browser tests use the same records and the unchanged encrypted fixture peer. Claude compatibility uses explicit expected parser output and the existing Claude suites.
