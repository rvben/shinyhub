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
