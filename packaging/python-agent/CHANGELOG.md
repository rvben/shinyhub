# Changelog

## [Unreleased]

### Added

- Experimental generated views: with `register(..., view=AgentView(...))` and
  `agent_view_output(id)`, the chat agent can compose up to six live tiles
  (stat, bar, line, table) on the app page from measures and dimensions the
  app declares. The app's query callback computes every value inside a Shiny
  output, so tiles follow page inputs without another chat turn.

## [0.5.4](https://github.com/rvben/shinyhub/compare/shinyhub-agent-v0.5.3...shinyhub-agent-v0.5.4) - 2026-11-27

### Fixed

- **agent**: support Shiny 1.9 and version browser assets ([2c55a35](https://github.com/rvben/shinyhub/commit/2c55a35bdb547f628c7cb4cbf027bc5cf25e82a9))

## [0.5.3](https://github.com/rvben/shinyhub/compare/shinyhub-agent-v0.5.2...shinyhub-agent-v0.5.3) - 2026-11-25

### Added

- Add an optional Bedrock request policy that checks the complete request
  before sending it, including tool results, retries, and suggestion recovery.

### Fixed

- Omit viewer usernames and raw chat failure details from built-in telemetry.
  Explicit usage callbacks still receive configured viewer attribution.
- Constrain Shiny to versions with supported reactive behavior.

## [0.5.2](https://github.com/rvben/shinyhub/compare/shinyhub-agent-v0.5.1...shinyhub-agent-v0.5.2) - 2026-10-26

### Fixed

- **bookmarks**: keep private inputs and agent payloads out of view URLs ([5ca0c99](https://github.com/rvben/shinyhub/commit/5ca0c99597bda072c05a29d547df59245cb77068))

## [0.5.1](https://github.com/rvben/shinyhub/compare/shinyhub-agent-v0.5.0...shinyhub-agent-v0.5.1) - 2026-10-23

### Fixed

- Bedrock retries a failed final answer once with completed tool results and
  tools disabled, except for permanent validation, access, and missing-model
  errors. Failed attempts do not leak partial answers or repeat app actions.
  Each attempt has its own usage record and sequential call round number.
- Final Bedrock answers and fallbacks start a new paragraph after streamed text.
  Failure logs include bounded exception messages, exception types, and turn IDs.
- Action-limit fallbacks across backends suggest asking again when reads or
  writes succeeded, and acknowledge actions that are already applied.

- **agent**: allow selecting chat text in the toolbar ([37a8e84](https://github.com/rvben/shinyhub/commit/37a8e84fd63dae7b0191f84dab08ef2778cdfee3))
- **ci**: use portable temporary directory for chart checks ([2258bfd](https://github.com/rvben/shinyhub/commit/2258bfd148a7347554a8e4ab7c805ad42c24d59a))

## [0.5.0](https://github.com/rvben/shinyhub/compare/shinyhub-agent-v0.4.2...shinyhub-agent-v0.5.0) - 2026-10-20

### Added

- Optional `AgentTool.undo_valid(arguments, result)` callback withdraws Undo
  when reactive app state makes the action stale. The synchronous, read-only
  callback tracks its reactive dependencies; returning `False` permanently
  removes the action's Undo button. Keep the stale-state check in the undo
  handler as well.

### Changed

- `register(..., undo_timeout=None)` now keeps Undo available without a time
  limit, until the next write, chat/session reset, or reactive withdrawal.
  Set `undo_timeout=300` to retain the previous five-minute window, or choose
  another finite duration of at least one second.

### Fixed

- Configured undo deadlines now reach the browser. Expired receipts remove
  the button and show "Undo no longer available", including when background
  timers are delayed.
- Expected `ToolError` failures from undo show the app's message and log at
  info level. Unexpected failures retain generic text and exception logging.
- Document that tool and undo callbacks run under the reactive lock:
  report values sent through `ui.update_*()` instead of waiting for an input
  echo, which cannot arrive until the callback returns.

## [0.4.2](https://github.com/rvben/shinyhub/compare/shinyhub-agent-v0.4.1...shinyhub-agent-v0.4.2) - 2026-10-16

### Added

- Experimental inline charts, enabled explicitly with `register(..., charts=True)`.
  App-owned read tools return validated `ChartData`; the agent can create bar,
  line, area and scatter charts and reuse session-local datasets for follow-ups.
- Interactive chart cards with hover details, expansion, SVG download and paged
  source data. Pinned chart libraries and licenses ship in the package and load
  from the app only when needed.
- Bounded datasets, fixed renderer specifications, safe failure and retry behavior,
  session isolation and reset handling. Existing write approvals remain in force.
- Independent agent releases through `vership --project agent`, with package-local
  version files, changelog, checks, tag namespace and PyPI verification.

Charts remain disabled by default. Deployments must permit the renderer's CSP
requirements; source data remains available when rendering is blocked. Validate
model tool behavior against the intended deployment before enabling charts.

- **agent**: add experimental opt-in inline charts ([eb86fca](https://github.com/rvben/shinyhub/commit/eb86fcaa41bf58355e87e7faf9e8c894d5ce0ea0))

## 0.4.1 — 2026-10-16

- Recover answers when Bedrock or Responses-compatible models return only
  follow-up suggestions, using at most one additional model request. Preserve
  native tool-result transcripts, validate metadata before replay, and never
  execute app tools during recovery.
- Show saved suggestions only after a complete, nonempty answer. Preserve the
  existing one-request path for answers that already include suggestions, and
  keep cancellation, error, and expired-approval suppression intact.
- Add recovery and lifecycle regression coverage and correct the opt-in
  compatibility probes' usage outcome and request-count checks.

## 0.4.0 — 2026-10-16

- Add optional app-defined starter questions through `register(..., starters=...)`.
  Keep the built-in questions by default, or hide them with an empty list.
- Add experimental, opt-in follow-up suggestions through
  `register(..., followups=1..3)` for Bedrock and Responses-compatible adapters.
  Suggestions fill the composer for editing and require an explicit send.
- Validate and isolate suggestions per session, show them only after successful
  answers, and preserve app-tool budgets, write approvals, and usage attribution.
  Add deterministic protocol, browser, and opt-in real-model compatibility tests.
- Raise minimum dependencies to `jsonschema>=4.26`, `httpx>=0.28`, and optional
  `boto3>=1.43`. Existing apps can leave follow-ups disabled (the default).

## 0.3.0 — 2026-10-15

- Export `ResponsesChat` as a protocol-oriented name for `OpenAIChat`, preserving
  the original name, class identity, arguments and defaults. Document adapter
  protocols separately from gateway routing policies.
- Support OpenAI-compatible Responses gateways with a validated `base_url`,
  opt-in server-supplied end-user attribution, and a configurable usage provider.
- Show fixed visitor messages for recognized gateway budget errors and missing
  required end-user identity, preserving applied-action receipts.
- Handle bounded multiline Responses SSE and tolerate malformed optional usage
  metadata without discarding valid answers. Exercise the real HTTP transport
  locally, with explicit live checks for fresh zero-budget users.
- Add optional `register(..., notice=...)` plain-text panel notices that persist
  across new chats and never enter model requests or usage token counts.

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
