from __future__ import annotations

import json
from collections.abc import AsyncIterator, Sequence
from dataclasses import dataclass, field
from typing import Any

import httpx

from ._core import ToolError, ToolRegistry
from ._formatting import panel_instructions
from ._runtime import (
    FINAL_INSTRUCTIONS,
    Approve,
    ToolCall,
    Turn,
    bounded_history,
    bounded_int,
    check_calls,
    validate_limits,
)
from ._usage import ModelCall, UsageCallback


@dataclass(frozen=True)
class OpenAIChat:
    """Server-side Responses API agent using the app's registered tools."""

    api_key: str = field(repr=False)
    instructions: str
    model: str = "gpt-4.1-mini"
    max_output_tokens: int = 500
    max_tool_calls_per_step: int = 2
    max_tool_rounds: int = 4
    max_tool_calls_per_turn: int = 8
    on_usage: UsageCallback | None = field(default=None, repr=False, compare=False)

    def __post_init__(self) -> None:
        if not self.api_key.strip():
            raise ValueError("An OpenAI API key is required")
        if not self.instructions.strip():
            raise ValueError("App-specific agent instructions are required")
        bounded_int("max_output_tokens", self.max_output_tokens, 100, 8_192)
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
        output = None
        text_seen = False
        total_bytes = 0
        async with client.stream(
            "POST", "https://api.openai.com/v1/responses", headers=headers, json=request
        ) as response:
            response.raise_for_status()
            async for line in response.aiter_lines():
                total_bytes += len(line.encode())
                if total_bytes > 1_048_576 or len(line.encode()) > 65_536:
                    raise ToolError(
                        "model_too_large", "The assistant sent too much data."
                    )
                if not line.startswith("data:"):
                    continue
                data = line[5:].strip()
                if data == "[DONE]":
                    continue
                event = json.loads(data)
                kind = event.get("type")
                if kind == "response.output_text.delta":
                    delta = event.get("delta")
                    if isinstance(delta, str) and delta:
                        text_seen = True
                        yield {"type": "delta", "text": delta}
                elif (
                    kind == "response.output_item.added"
                    and event.get("item", {}).get("type") == "function_call"
                ):
                    name = event["item"].get("name")
                    if isinstance(name, str):
                        usage.tool_names += (name,)
                    yield {"type": "status", "text": "Using app tools"}
                elif kind in (
                    "response.completed",
                    "response.incomplete",
                    "response.failed",
                ):
                    body = event.get("response", {})
                    tokens = body.get("usage") or {}
                    usage.tokens = {
                        "input_tokens": tokens.get("input_tokens"),
                        "output_tokens": tokens.get("output_tokens"),
                        "cache_read_tokens": (
                            tokens.get("input_tokens_details") or {}
                        ).get("cached_tokens"),
                    }
                    if kind != "response.completed":
                        raise ToolError(
                            "model_failed", "The assistant could not finish its reply."
                        )
                    output = body.get("output", [])
                elif kind == "error":
                    raise ToolError(
                        "model_failed", "The assistant could not finish its reply."
                    )
        if not isinstance(output, list):
            raise ToolError("model_failed", "The assistant connection ended early.")
        calls = []
        for item in output:
            if item.get("type") != "function_call":
                continue
            raw = item.get("arguments", "")
            if not isinstance(raw, str) or len(raw.encode()) > 8_192:
                raise ToolError(
                    "model_too_large", "The assistant sent too much tool data."
                )
            try:
                arguments = json.loads(raw)
            except ValueError as error:
                raise ToolError(
                    "model_failed", "The assistant sent invalid tool arguments."
                ) from error
            calls.append(ToolCall(item.get("call_id"), item.get("name"), arguments))
        check_calls(calls)
        usage.tool_names = tuple(call.name for call in calls)
        if not calls and not text_seen:
            answer = "\n".join(
                part["text"]
                for item in output
                if item.get("type") == "message"
                for part in item.get("content", [])
                if part.get("type") == "output_text"
                and isinstance(part.get("text"), str)
            ).strip()
            if not answer:
                raise ToolError("model_failed", "The assistant returned no answer.")
            yield {"type": "delta", "text": answer}
        yield {"type": "model_response", "output": output, "calls": calls}

    async def run(
        self,
        message: str,
        history: Sequence[dict[str, str]],
        tools: ToolRegistry,
        approve: Approve,
        *,
        thread_id: str,
    ) -> AsyncIterator[dict[str, Any]]:
        if not isinstance(message, str) or not 1 <= len(message.strip()) <= 2_000:
            raise ToolError(
                "invalid_message", "Enter a question of at most 2,000 characters."
            )
        inputs: list[dict[str, Any]] = bounded_history(history)
        inputs.append({"role": "user", "content": message.strip()})
        model_tools = [
            {
                "type": "function",
                "name": item.name,
                "description": item.description,
                "parameters": dict(item.input_schema),
                "strict": False,
            }
            for item in (
                tools.get(spec["name"]) for spec in tools.public_spec()["tools"]
            )
            if item is not None
        ]
        headers = {
            "Authorization": f"Bearer {self.api_key}",
            "Content-Type": "application/json",
        }
        turn = Turn(self.max_tool_calls_per_step, self.max_tool_calls_per_turn)
        async with httpx.AsyncClient(timeout=httpx.Timeout(45, connect=5)) as client:
            for round_index in range(self.max_tool_rounds + 1):
                final = round_index == self.max_tool_rounds or turn.exhausted
                instructions = panel_instructions(self.instructions)
                if final:
                    instructions += "\n\n" + FINAL_INSTRUCTIONS
                request = {
                    "model": self.model,
                    "instructions": instructions,
                    "input": inputs,
                    "tools": model_tools,
                    "parallel_tool_calls": False,
                    "max_output_tokens": self.max_output_tokens,
                    "store": False,
                    "stream": True,
                }
                if final:
                    request["tool_choice"] = "none"
                buffered = []
                try:
                    async with ModelCall(
                        "openai",
                        self.model,
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
                                "model_failed",
                                "The assistant requested tools after its budget ended.",
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
                inputs.extend(response["output"])
                async for event in turn.execute(response["calls"], tools, approve):
                    if event["type"] == "tool_result":
                        inputs.append(
                            {
                                "type": "function_call_output",
                                "call_id": event["id"],
                                "output": json.dumps(event["result"]),
                            }
                        )
                    else:
                        yield event
                yield {"type": "status", "text": "Writing answer"}
