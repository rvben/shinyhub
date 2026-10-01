from __future__ import annotations

import json
import secrets
from collections.abc import AsyncIterator
from dataclasses import dataclass, field
from typing import Any
from urllib.parse import urlsplit

import httpx

from ._core import ToolError, ToolRegistry
from ._runtime import (
    FINAL_INSTRUCTIONS,
    Approve,
    ToolCall,
    Turn,
    bounded_history,
    check_calls,
    validate_limits,
)
from ._usage import ModelCall, UsageCallback

MAX_EVENT_BYTES = 65_536
MAX_RUN_BYTES = 1_048_576


@dataclass(frozen=True)
class AGUIChat:
    """Connect a hoster-owned AG-UI endpoint to one Shiny viewer session."""

    endpoint: str
    bearer_token: str = field(default="", repr=False)

    max_tool_calls_per_step: int = 2
    max_tool_rounds: int = 4
    max_tool_calls_per_turn: int = 8
    on_usage: UsageCallback | None = field(default=None, repr=False, compare=False)

    def __post_init__(self) -> None:
        parsed = urlsplit(self.endpoint)
        if (
            parsed.scheme != "https"
            or not parsed.netloc
            or parsed.username
            or parsed.password
        ):
            raise ValueError(
                "AG-UI endpoints must be HTTPS URLs without embedded credentials"
            )
        validate_limits(
            self.max_tool_calls_per_step,
            self.max_tool_rounds,
            self.max_tool_calls_per_turn,
        )
        if self.on_usage is not None and not callable(self.on_usage):
            raise TypeError("on_usage must be callable")

    async def _response(
        self,
        client: httpx.AsyncClient,
        headers: dict[str, str],
        request: dict[str, Any],
        usage: ModelCall,
    ) -> AsyncIterator[dict[str, Any]]:
        calls: dict[str, dict[str, Any]] = {}
        run_finished = False
        text_seen = False
        total = 0
        async with client.stream(
            "POST", self.endpoint, headers=headers, json=request
        ) as response:
            response.raise_for_status()
            data_lines: list[str] = []
            async for line in response.aiter_lines():
                total += len(line.encode())
                if total > MAX_RUN_BYTES:
                    raise ToolError("agent_too_large", "The agent sent too much data.")
                if line.startswith("data:"):
                    data_lines.append(line[5:].lstrip())
                    if sum(map(len, data_lines)) > MAX_EVENT_BYTES:
                        raise ToolError(
                            "agent_too_large", "The agent event was too large."
                        )
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
                        raise ToolError(
                            "bad_agent_event", "The agent sent an invalid tool call."
                        )
                    if call_id in calls:
                        raise ToolError(
                            "bad_agent_event", "The agent repeated a tool call."
                        )
                    calls[call_id] = {
                        "name": event.get("toolCallName"),
                        "arguments": "",
                        "ended": False,
                    }
                    yield {"type": "status", "text": "Using app tools"}
                elif kind == "TOOL_CALL_ARGS":
                    call = calls.get(event.get("toolCallId"))
                    if call is None or not isinstance(event.get("delta"), str):
                        raise ToolError(
                            "bad_agent_event", "The agent sent invalid tool arguments."
                        )
                    call["arguments"] += event["delta"]
                    if len(call["arguments"].encode()) > 8_192:
                        raise ToolError(
                            "bad_agent_event",
                            "The agent tool arguments were too large.",
                        )
                elif kind == "TOOL_CALL_END":
                    call = calls.get(event.get("toolCallId"))
                    if call is None:
                        raise ToolError(
                            "bad_agent_event", "The agent ended an unknown tool call."
                        )
                    call["ended"] = True
                elif kind == "TOOL_CALL_RESULT":
                    calls.pop(event.get("toolCallId"), None)
                elif kind == "RUN_ERROR":
                    raise ToolError(
                        "agent_failed", "The connected agent could not finish."
                    )
                elif kind == "RUN_FINISHED":
                    run_finished = True
        if not run_finished:
            raise ToolError("agent_failed", "The connected agent ended early.")
        if not calls and not text_seen:
            raise ToolError("agent_failed", "The connected agent returned no answer.")
        if any(not call["ended"] for call in calls.values()):
            raise ToolError("bad_agent_event", "The agent sent incomplete tool calls.")
        normalized = []
        for call_id, call in calls.items():
            try:
                arguments = json.loads(call["arguments"])
            except ValueError as error:
                raise ToolError(
                    "bad_agent_event", "The agent sent invalid tool arguments."
                ) from error
            normalized.append(ToolCall(call_id, call["name"], arguments))
        check_calls(normalized)
        usage.tool_names = tuple(call.name for call in normalized)
        yield {"type": "model_response", "calls": normalized}

    async def run(
        self,
        message: str,
        history: list[dict[str, str]],
        tools: ToolRegistry,
        approve: Approve,
        *,
        thread_id: str,
    ) -> AsyncIterator[dict[str, Any]]:
        if not isinstance(message, str) or not 1 <= len(message.strip()) <= 2_000:
            raise ToolError(
                "invalid_message", "Enter a question of at most 2,000 characters."
            )
        messages = [
            {"id": secrets.token_urlsafe(10), **item}
            for item in bounded_history(history)
        ]
        messages.append(
            {
                "id": secrets.token_urlsafe(10),
                "role": "user",
                "content": message.strip(),
            }
        )
        offered = [
            {
                "name": spec["name"],
                "description": spec["description"],
                "parameters": spec["inputSchema"],
            }
            for spec in tools.public_spec()["tools"]
        ]
        headers = {"Accept": "text/event-stream", "Content-Type": "application/json"}
        if self.bearer_token:
            headers["Authorization"] = f"Bearer {self.bearer_token}"
        turn = Turn(self.max_tool_calls_per_step, self.max_tool_calls_per_turn)
        async with httpx.AsyncClient(timeout=httpx.Timeout(45, connect=5)) as client:
            for round_index in range(self.max_tool_rounds + 1):
                final = round_index == self.max_tool_rounds or turn.exhausted
                request = {
                    "threadId": thread_id,
                    "runId": secrets.token_urlsafe(16),
                    "state": {},
                    "messages": messages,
                    "tools": [] if final else offered,
                    "context": [
                        {"description": "App tool budget", "value": FINAL_INSTRUCTIONS}
                    ]
                    if final
                    else [],
                    "forwardedProps": {},
                }
                buffered = []
                try:
                    async with ModelCall(
                        "agui",
                        "external",
                        turn.id,
                        round_index + 1,
                        final,
                        self.on_usage,
                    ) as usage:
                        async for event in self._response(
                            client, headers, request, usage
                        ):
                            if event["type"] == "model_response":
                                response = event
                            elif final:
                                buffered.append(event)
                            else:
                                yield event
                        if final and response["calls"]:
                            raise ToolError(
                                "agent_failed",
                                "The agent requested tools after its budget ended.",
                            )
                except Exception:
                    if not final:
                        raise
                    yield {"type": "delta", "text": turn.fallback()}
                    return
                if final:
                    for event in buffered:
                        yield event
                    return
                if not response["calls"]:
                    return
                async for event in turn.execute(response["calls"], tools, approve):
                    if event["type"] == "tool_result":
                        messages.append(
                            {
                                "id": secrets.token_urlsafe(10),
                                "role": "tool",
                                "toolCallId": event["id"],
                                "content": json.dumps(event["result"]),
                            }
                        )
                    else:
                        yield event
                yield {"type": "status", "text": "Writing answer"}
