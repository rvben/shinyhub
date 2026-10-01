# Changelog

## 0.2.0 — 2026-10-01

First regular release of the 0.2 series, following 0.2.0b4.

- Show nested approval arguments as plain text, with complete details available
  for shortened lists and custom descriptions. Display approval deadlines and
  remove expired decisions.
- Add `AgentTool.validate` before approval and again before execution, plus
  `AgentTool.describe` for action-specific confirmation details.
- Recover from excess tool calls using correlated deferred results. Configure
  step, turn and round budgets; reserve a final tools-disabled answer.
- Run registered callbacks in the Shiny session with isolated reads and
  automatic reactive flushing. Cancel outstanding tasks when the session ends.
- Configure approval timeouts, conversation history and answer limits.
- Attribute model calls through structured usage records, callbacks and tracing.
- Expose public chat-opening hooks for browser tests, including native toolbar
  chat with a closed shadow root.

### Migration

Browser tools now expose only reads by default. Apps that intentionally permit
browser writes must use `register(..., allow_browser_writes=True)`. Browser
confirmation is not a security boundary; consequential writes should use chat's
server-side approval and app permission checks.

Return normalized server-owned view state from write tools. Client input updates
remain asynchronous; there is no general app-wide settlement guarantee.

### Verification

Verified clean wheel and source installations, provider protocol and recovery
tests, per-viewer usage isolation, desktop/mobile browser accessibility, and a
real Shiny session covering approval, updates, undo and cancellation. Live model
behavior and the reporting client's dashboard were not part of this verification.
