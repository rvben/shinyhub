from __future__ import annotations

import asyncio
import json
from collections.abc import AsyncIterator, Sequence
from dataclasses import dataclass, field
from typing import Any

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

_END = object()


@dataclass(frozen=True)
class BedrockChat:
    """Stream an app-scoped agent through Amazon Bedrock ConverseStream."""

    model_id: str
    instructions: str
    region: str
    max_output_tokens: int = 500
    client: Any = field(default=None, repr=False, compare=False)
    max_tool_calls_per_step: int = 2
    max_tool_rounds: int = 4
    max_tool_calls_per_turn: int = 8
    on_usage: UsageCallback | None = field(default=None, repr=False, compare=False)

    def __post_init__(self) -> None:
        if (
            not self.model_id.strip()
            or not self.instructions.strip()
            or not self.region.strip()
        ):
            raise ValueError(
                "Bedrock model ID, instructions, and AWS region are required"
            )
        bounded_int("max_output_tokens", self.max_output_tokens, 100, 8_192)
        validate_limits(
            self.max_tool_calls_per_step,
            self.max_tool_rounds,
            self.max_tool_calls_per_turn,
        )
        if self.on_usage is not None and not callable(self.on_usage):
            raise TypeError("on_usage must be callable")

    def _runtime(self) -> Any:
        if self.client is not None:
            return self.client
        try:
            import boto3
            from botocore.config import Config
        except ImportError as error:
            raise ToolError(
                "bedrock_unavailable",
                "Install shinyhub-agent[bedrock] to use Amazon Bedrock.",
            ) from error
        return boto3.client(
            "bedrock-runtime",
            region_name=self.region,
            config=Config(
                connect_timeout=5, read_timeout=45, retries={"max_attempts": 2}
            ),
        )

    async def _events(
        self, client: Any, request: dict[str, Any]
    ) -> AsyncIterator[dict[str, Any]]:
        response = await asyncio.to_thread(client.converse_stream, **request)
        stream = response["stream"]
        iterator = iter(stream)
        try:
            while True:
                event = await asyncio.to_thread(next, iterator, _END)
                if event is _END:
                    return
                yield event
        finally:
            close = getattr(stream, "close", None)
            if callable(close):
                await asyncio.to_thread(close)

    async def _response(
        self, client: Any, request: dict[str, Any], record: ModelCall
    ) -> AsyncIterator[dict[str, Any]]:
        blocks: dict[int, dict[str, Any]] = {}
        argument_chunks: dict[int, str] = {}
        stop_reason: str | None = None
        text_seen = False
        total_bytes = 0
        async for event in self._events(client, request):
            encoded_size = len(json.dumps(event, default=str).encode())
            total_bytes += encoded_size
            if encoded_size > 65_536 or total_bytes > 1_048_576:
                raise ToolError("model_too_large", "The assistant sent too much data.")
            if "messageStart" in event:
                if event["messageStart"].get("role") != "assistant":
                    raise ToolError(
                        "model_failed", "The assistant sent an invalid response."
                    )
            elif "contentBlockStart" in event:
                part = event["contentBlockStart"]
                index = part.get("contentBlockIndex")
                use = part.get("start", {}).get("toolUse")
                if not isinstance(index, int) or not isinstance(use, dict):
                    raise ToolError(
                        "model_failed", "The assistant sent an invalid tool call."
                    )
                tool_id, name = use.get("toolUseId"), use.get("name")
                if (
                    not isinstance(tool_id, str)
                    or not 1 <= len(tool_id) <= 128
                    or not isinstance(name, str)
                ):
                    raise ToolError(
                        "model_failed", "The assistant sent an invalid tool call."
                    )
                if index in blocks:
                    raise ToolError(
                        "model_failed", "The assistant repeated a tool block."
                    )
                record.tool_names += (name,)
                blocks[index] = {"toolUse": {"toolUseId": tool_id, "name": name}}
                argument_chunks[index] = ""
                yield {"type": "status", "text": "Using app tools"}
            elif "contentBlockDelta" in event:
                part = event["contentBlockDelta"]
                index = part.get("contentBlockIndex")
                delta = part.get("delta", {})
                if not isinstance(index, int) or not isinstance(delta, dict):
                    raise ToolError(
                        "model_failed", "The assistant sent an invalid response."
                    )
                if isinstance(delta.get("text"), str) and delta["text"]:
                    text_seen = True
                    block = blocks.setdefault(index, {"text": ""})
                    if "text" not in block:
                        raise ToolError(
                            "model_failed", "The assistant sent an invalid response."
                        )
                    block["text"] += delta["text"]
                    yield {"type": "delta", "text": delta["text"]}
                elif isinstance(delta.get("toolUse"), dict):
                    chunk = delta["toolUse"].get("input")
                    if index not in argument_chunks or not isinstance(chunk, str):
                        raise ToolError(
                            "model_failed", "The assistant sent invalid tool arguments."
                        )
                    argument_chunks[index] += chunk
                    if len(argument_chunks[index].encode()) > 8_192:
                        raise ToolError(
                            "model_too_large", "The assistant sent too much tool data."
                        )
            elif "messageStop" in event:
                stop_reason = event["messageStop"].get("stopReason")
            elif "metadata" in event:
                usage = event["metadata"].get("usage", {})
                if usage:
                    record.tokens = {
                        "input_tokens": usage.get("inputTokens"),
                        "output_tokens": usage.get("outputTokens"),
                        "cache_read_tokens": usage.get("cacheReadInputTokens"),
                        "cache_write_tokens": usage.get("cacheWriteInputTokens"),
                    }
            elif any(key.endswith("Exception") for key in event):
                raise ToolError(
                    "model_failed", "The assistant could not finish its reply."
                )

        for index, raw in argument_chunks.items():
            try:
                arguments = json.loads(raw or "{}")
            except ValueError as error:
                raise ToolError(
                    "model_failed", "The assistant sent invalid tool arguments."
                ) from error
            blocks[index]["toolUse"]["input"] = arguments
        content = [blocks[index] for index in sorted(blocks)]
        calls = [part["toolUse"] for part in content if "toolUse" in part]
        normalized = [
            ToolCall(call["toolUseId"], call["name"], call["input"]) for call in calls
        ]
        check_calls(normalized)
        record.tool_names = tuple(call.name for call in normalized)
        if calls:
            if stop_reason != "tool_use":
                raise ToolError(
                    "model_failed", "The assistant sent incomplete tool calls."
                )
        elif stop_reason not in ("end_turn", "stop_sequence") or not text_seen:
            raise ToolError("model_failed", "The assistant did not finish its reply.")
        yield {"type": "model_response", "content": content, "calls": normalized}

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
        messages = [
            {"role": item["role"], "content": [{"text": item["content"]}]}
            for item in bounded_history(history)
        ]
        messages.append({"role": "user", "content": [{"text": message.strip()}]})
        tool_specs = [
            {
                "toolSpec": {
                    "name": spec["name"],
                    "description": spec["description"],
                    "inputSchema": {"json": spec["inputSchema"]},
                }
            }
            for spec in tools.public_spec()["tools"]
        ]
        client = self._runtime()
        turn = Turn(self.max_tool_calls_per_step, self.max_tool_calls_per_turn)
        for round_index in range(self.max_tool_rounds + 1):
            final = round_index == self.max_tool_rounds or turn.exhausted
            instructions = panel_instructions(self.instructions)
            request = {
                "modelId": self.model_id,
                "system": [{"text": instructions}],
                "messages": messages,
                "inferenceConfig": {"maxTokens": self.max_output_tokens},
            }
            if final:
                request["system"] = [
                    {"text": instructions + "\n\n" + FINAL_INSTRUCTIONS}
                ]
                # Converse has no portable tool-choice "none". Convert the completed
                # tool transcript to text before omitting toolConfig; toolUse/toolResult
                # blocks require toolConfig even when they only describe past actions.
                request["messages"] = [
                    {
                        "role": item["role"],
                        "content": [
                            {
                                "text": "\n".join(
                                    block["text"]
                                    if "text" in block
                                    else json.dumps(block)
                                    for block in item["content"]
                                )
                            }
                        ],
                    }
                    for item in messages
                ]
            else:
                request["toolConfig"] = {"tools": tool_specs}
            buffered = []
            try:
                async with ModelCall(
                    "bedrock",
                    self.model_id,
                    turn.id,
                    round_index + 1,
                    final,
                    self.on_usage,
                ) as usage:
                    async for event in self._response(client, request, usage):
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
            messages.append({"role": "assistant", "content": response["content"]})
            results = []
            async for event in turn.execute(response["calls"], tools, approve):
                if event["type"] == "tool_result":
                    results.append(
                        {
                            "toolResult": {
                                "toolUseId": event["id"],
                                "content": [{"json": event["result"]}],
                                "status": "success" if event["ok"] else "error",
                            }
                        }
                    )
                else:
                    yield event
            messages.append({"role": "user", "content": results})
            yield {"type": "status", "text": "Writing answer"}
