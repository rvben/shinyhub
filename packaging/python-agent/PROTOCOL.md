# ShinyHub agent session protocol, version 1

This document describes the browser-to-app message boundary implemented by
`shinyhub-agent`. It is intentionally scoped to **one Shiny viewer session**.
It is not an HTTP API, an MCP server, or an authorization token format.

The app sends custom messages using `session.send_custom_message`. The browser
sends inputs with `Shiny.setInputValue(..., {priority: "event"})`. Every request
and result carries `version: 1` and the session nonce issued by the app. The
nonce detects a stale or reconnected session; it does not authorize a user.
ShinyHub and the app handler enforce access to app data and actions.

## Tool discovery

Browser discovery exposes only read-only tools by default. The server also
rejects direct browser requests for write tools with `write_not_allowed`.
An app can explicitly enable browser writes with
`register(..., allow_browser_writes=True)`; the write examples below assume
that opt-in. Chat retains its full tool registry and server-side write
approval independently of the browser setting.

The browser sends `.shinyhub_agent_discover` with `{version: 1, nonce: <time>}`
after its bridge loads. The app also publishes capabilities after its first
Shiny flush. The app replies with `shinyhub-agent-capabilities`:

```json
{
  "version": 1,
  "session": "opaque-random-session-nonce",
  "tools": [{
    "name": "set_period",
    "description": "Change the current viewer's reporting period",
    "inputSchema": {
      "type": "object",
      "properties": {"period": {"type": "string", "enum": ["week", "year"]}},
      "required": ["period"],
      "additionalProperties": false
    },
    "readOnly": false,
    "confirmation": "Change this dashboard's reporting period?"
  }]
}
```

Tool names use lower-case ASCII letters, digits, and underscores. Schemas are
JSON Schema Draft 2020-12 objects without `$ref` or `$dynamicRef`. Tool
descriptions, schemas, and confirmation text are public to the page and agent.
Do not put secrets or hidden instructions in them.

## Tool call and result

The browser sends `.shinyhub_agent_request`:

```json
{
  "version": 1,
  "session": "opaque-random-session-nonce",
  "requestId": "client-generated-unique-id",
  "name": "set_period",
  "arguments": {"period": "year"}
}
```

The app validates the nonce, tool name, and arguments, executes a handler in
that viewer's Shiny session, and sends `shinyhub-agent-result` with the same
`session` and `requestId`:

```json
{"version":1,"session":"opaque-random-session-nonce","requestId":"client-generated-unique-id","ok":true,"result":{"period":"year"}}
```

Failures use `ok: false`, a stable `code`, and a visitor-safe `message`. A
handler exception is never serialized. A successful result means the handler
completed; app authors should return authoritative applied state, not a
requested value that may still be pending in the UI.

Opted-in browser writes first send `action: "prepare"` with the same request
fields. The server validates the schema and dynamic app constraints without
running the handler, then returns `{arguments, confirmation, description}`.
The bridge displays that snapshot before sending `action: "execute"` (also
the default when absent). Execution revalidates current constraints. This
preflight improves confirmation; it does not create an authorization token
or make browser approval a server-side security boundary.

## Chat

When a chat backend is configured, the app publishes
`shinyhub-agent-chat-capabilities` with `{version: 1, session: <separate nonce>,
enabled: true}`. The browser sends `.shinyhub_agent_chat_request` with the
nonce, a unique `requestId`, and a `message`. The server owns conversation
history; it ignores any history supplied by the browser. It streams
`shinyhub-agent-chat-event` messages with matching `version`, `session`, and
`requestId` for `status`, `delta`, `action_applied`, `done`, and `error`.

Provider failures may add an error `code` of `budget_exceeded` or
`end_user_required`, with a fixed visitor-safe `message`. Clients display the
message as text and end the active request. Gateway configuration and end-user
identity remain server-side; browser messages cannot supply attribution.

Capabilities may include `notice`, an optional app-owned plain-text string of
at most 1,000 characters. It is panel configuration, never conversation history
or model input. The browser renders it outside the message log and fallback
header, preserving it across resets and in native layouts. Each capabilities
message replaces the current notice; an absent or blank notice removes it.
Older clients ignore this additive field.

For a write, the server validates the schema and app constraints, then emits
`approval_required` with an `approvalId`, tool name, arguments, display
`message`, optional plain-text `description` and `expiresIn` in seconds.
Validation errors go to the model as structured tool results without a card.
The browser sends
`.shinyhub_agent_chat_decision` with the chat nonce, approval ID, and boolean
`approved`. The server accepts only the currently pending ID, executes the
registered handler after approval, and emits `action_applied` with its result.
Execution revalidates the approved snapshot. Denial or timeout does not run
the handler. Timeout emits `approval_expired` with the matching `approvalId`;
late decisions cannot revive that approval. The chat panel's **Stop** and
**New chat** controls send `action: "cancel"` and `action: "reset"` requests.
Every reset is answered with its `requestId`: a `reset` event once the
conversation is cleared, or an `error` event when an answer or Undo is still
running or the reset failed, in which case the conversation is unchanged. The
panel keeps the composer's draft unsent until that answer arrives.

For an undoable write, `action_applied` includes a plain-text `receipt`,
`actionId`, and `undoExpiresIn`: the remaining undo window in seconds when the
event is sent, or `null` when time-based expiry is disabled (the default).
A configured window starts when the write completes; delayed receipts may carry
zero remaining seconds. Clients remove expired Undo buttons and show
"Undo no longer available". A missing field from an older server uses the
legacy 300-second window. Each new write replaces the previous undo, even if
the new write has no undo handler. Reset and session changes also clear undo.

To undo, send `.shinyhub_agent_chat_request` with `action: "undo"`, `actionId`,
and the usual version, session and request ID. The server checks the current
action and deadline independently, then sends `undo_result` with `ok` and
either `detail` or a visitor-safe `message`. An expected `ToolError` includes
its `code` and message; unexpected exceptions are not serialized. An attempted
handler consumes the undo even when it fails.

An optional app `undo_valid` callback can withdraw undo on reactive state
changes. The server consumes the action and emits `undo_withdrawn` with
`version`, the chat `session`, `actionId`, and a plain-text `message`. This
notification is independent of a chat request and has no `requestId`. It is
sent after the corresponding receipt; clients remove only the matching
available Undo and show the message without replacing the receipt title.
Stale session/action notifications must not affect a newer receipt or an
undo already in flight. Withdrawal is permanent, even if the state becomes
valid again; the server rejects subsequent undo requests for that action.

The browser helper also announces `shinyhub:chat:capabilities` with
`{version: 1, enabled: true, panel: <Element>, title: <string>}`. A compatible
ShinyHub host slots that element into its own overlay and responds with
`shinyhub:chat:host` containing `native: true`. The helper then hides its
fallback header. The host sends `shinyhub:chat:toggle` and
`shinyhub:chat:new`; the helper reports `shinyhub:chat:state` and
`shinyhub:chat:busy` so the host can manage its overlay and disable **New**
while a request is active. Older hosts ignore the added fields and retain the
helper's own chat frame.

`window.shinyhubAgentChat.open()` and `.close()` are idempotent public hooks.
They return whether chat is available, work with native and fallback panels,
and never submit questions or approve actions.

## Generated view

A generated view is an ordinary Shiny output, not a custom message. The output
binding `shinyhub.agentView` binds elements with class `shinyhub-agent-view`
and receives `{version: 1, view: null}` when there is no view, or
`{version: 1, view: {title, tiles}}`. A payload with another version, or
without a `tiles` array, renders nothing and hides the element.

Each tile has `id`, `kind` (`stat`, `bar`, `line`, `table`), `title`, and
`state`. A tile narrowed by its own filters also has `scope`, an array of
`[label, value]` string pairs that clients display with the tile in every
state:

- `ok`: a stat carries `value` (a number or `null`) and `label`; a table
  carries `columns` (labels) and `rows` (arrays in column order); a chart
  carries `chart` with `kind`, `x`, `y`, optional `color`, `fields` (label to
  `nominal`, `quantitative` or `temporal`), and `rows` keyed by label.
- `empty`: no data for the current selection; no value fields are present.
- `error`: a plain-text `message` of at most 300 characters. Unexpected
  exceptions are logged server-side and replaced with a fixed message.

A `null` value is absent data. Clients render it as `n/a`, never as zero or
blank. Every string is rendered as text. Clients skip a redraw when the
payload is unchanged.

**Dismiss** sets the input `.shinyhub_agent_view_dismiss` with event priority;
the value carries no meaning. The server removes the view for that session and
the output then sends `view: null`. The input is excluded from bookmarks. The
layout itself is set only by the chat-only `build_view` and `clear_view` tools
on the server; no browser message can supply a layout or tile data.

## Compatibility rules

- Unknown message versions are ignored or rejected; changes to field meaning
  require a new protocol version.
- Unknown tool names and invalid arguments never reach app handlers.
- Results are correlated by both session nonce and request ID; a result from a
  previous Shiny session must not settle a current request.
- A WebMCP tool and a chat agent share the same server-side tool registry, but
  their browser confirmation flows are distinct.
- Browser confirmation is not a security boundary. The handler must check
  permissions and data scope for the current viewer.
