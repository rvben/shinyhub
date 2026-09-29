from __future__ import annotations

import json
import secrets
from collections.abc import AsyncIterator
from dataclasses import dataclass, field
from typing import Any
from urllib.parse import urlsplit

import httpx

from ._core import ToolError, ToolRegistry
from ._openai import Approve

MAX_EVENT_BYTES = 65_536
MAX_RUN_BYTES = 1_048_576


@dataclass(frozen=True)
class AGUIChat:
    """Connect a hoster-owned AG-UI endpoint to one Shiny viewer session."""

    endpoint: str
    bearer_token: str = field(default="", repr=False)

    def __post_init__(self) -> None:
        parsed = urlsplit(self.endpoint)
        if parsed.scheme != "https" or not parsed.netloc or parsed.username or parsed.password:
            raise ValueError("AG-UI endpoints must be HTTPS URLs without embedded credentials")

    async def run(self, message: str, history: list[dict[str, str]],
                  tools: ToolRegistry, approve: Approve, *, thread_id: str
                  ) -> AsyncIterator[dict[str, Any]]:
        if not isinstance(message, str) or not 1 <= len(message.strip()) <= 2_000:
            raise ToolError("invalid_message", "Enter a question of at most 2,000 characters.")
        messages = [
            {"id": secrets.token_urlsafe(10), "role": item["role"],
             "content": item["content"][:2_000]}
            for item in history[-12:]
            if item.get("role") in ("user", "assistant") and isinstance(item.get("content"), str)
        ]
        messages.append({"id": secrets.token_urlsafe(10), "role": "user", "content": message})
        offered = [{
            "name": spec["name"], "description": spec["description"],
            "parameters": spec["inputSchema"],
        } for spec in tools.public_spec()["tools"]]
        headers = {"Accept": "text/event-stream", "Content-Type": "application/json"}
        if self.bearer_token:
            headers["Authorization"] = f"Bearer {self.bearer_token}"
        async with httpx.AsyncClient(timeout=httpx.Timeout(45, connect=5)) as client:
            for _ in range(4):
                calls: dict[str, dict[str, Any]] = {}
                run_finished = False
                text_seen = False
                total = 0
                async with client.stream("POST", self.endpoint, headers=headers, json={
                    "threadId": thread_id,
                    "runId": secrets.token_urlsafe(16),
                    "state": {},
                    "messages": messages,
                    "tools": offered,
                    "context": [],
                    "forwardedProps": {},
                }) as response:
                    response.raise_for_status()
                    data_lines: list[str] = []
                    async for line in response.aiter_lines():
                        total += len(line.encode())
                        if total > MAX_RUN_BYTES:
                            raise ToolError("agent_too_large", "The agent sent too much data.")
                        if line.startswith("data:"):
                            data_lines.append(line[5:].lstrip())
                            if sum(map(len, data_lines)) > MAX_EVENT_BYTES:
                                raise ToolError("agent_too_large", "The agent event was too large.")
                            continue
                        if line or not data_lines:
                            continue
                        raw_event = "\n".join(data_lines)
                        data_lines.clear()
                        event = json.loads(raw_event)
                        kind = event.get("type")
                        if kind in ("TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_CHUNK"):
                            delta = event.get("delta")
                            if isinstance(delta, str) and delta:
                                text_seen = True
                                yield {"type": "delta", "text": delta}
                        elif kind == "RUN_STARTED":
                            yield {"type": "status", "text": "Agent is working"}
                        elif kind == "TOOL_CALL_START":
                            call_id = event.get("toolCallId")
                            if not isinstance(call_id, str) or len(call_id) > 100:
                                raise ToolError("bad_agent_event", "The agent sent an invalid tool call.")
                            calls[call_id] = {"name": event.get("toolCallName"), "arguments": "", "ended": False}
                            yield {"type": "status", "text": "Using app tools"}
                        elif kind == "TOOL_CALL_ARGS":
                            call = calls.get(event.get("toolCallId"))
                            if call is None or not isinstance(event.get("delta"), str):
                                raise ToolError("bad_agent_event", "The agent sent invalid tool arguments.")
                            call["arguments"] += event["delta"]
                            if len(call["arguments"].encode()) > 8_192:
                                raise ToolError("bad_agent_event", "The agent tool arguments were too large.")
                        elif kind == "TOOL_CALL_END":
                            call = calls.get(event.get("toolCallId"))
                            if call is None:
                                raise ToolError("bad_agent_event", "The agent ended an unknown tool call.")
                            call["ended"] = True
                        elif kind == "TOOL_CALL_RESULT":
                            calls.pop(event.get("toolCallId"), None)
                        elif kind == "RUN_ERROR":
                            raise ToolError("agent_failed", "The connected agent could not finish.")
                        elif kind == "RUN_FINISHED":
                            run_finished = True
                if not run_finished:
                    raise ToolError("agent_failed", "The connected agent ended early.")
                if not calls:
                    if not text_seen:
                        raise ToolError("agent_failed", "The connected agent returned no answer.")
                    return
                if len(calls) > 2 or any(not call["ended"] for call in calls.values()):
                    raise ToolError("bad_agent_event", "The agent sent incomplete tool calls.")
                for call_id, call in calls.items():
                    tool = tools.get(call["name"])
                    try:
                        arguments = json.loads(call["arguments"])
                        if tool is None:
                            result: Any = {"error": "Tool unavailable"}
                        else:
                            yield {"type": "tool_started", "name": tool.name,
                                   "description": tool.description, "readOnly": tool.read_only}
                            if tool.read_only:
                                result = await tools.execute(tool.name, arguments)
                            else:
                                result = await approve(tool.name, arguments)
                                yield {"type": "action_applied", "name": tool.name, "result": result}
                            yield {"type": "tool_finished", "name": tool.name, "ok": True}
                    except (ValueError, ToolError) as error:
                        result = {"error": str(error)}
                        if tool is not None:
                            yield {"type": "tool_finished", "name": tool.name, "ok": False}
                    messages.append({
                        "id": secrets.token_urlsafe(10), "role": "tool",
                        "toolCallId": call_id, "content": json.dumps(result),
                    })
                yield {"type": "status", "text": "Writing answer"}
        raise ToolError("agent_failed", "The connected agent did not finish.")
