from __future__ import annotations

import asyncio
import json
import logging
from collections.abc import AsyncIterator, Awaitable, Callable, Sequence
from dataclasses import dataclass, field
from typing import Any

from ._core import ToolError, ToolRegistry

Approve = Callable[[str, dict[str, Any]], Awaitable[Any]]
logger = logging.getLogger(__name__)
_END = object()


@dataclass(frozen=True)
class BedrockChat:
    """Stream an app-scoped agent through Amazon Bedrock ConverseStream."""

    model_id: str
    instructions: str
    region: str
    max_output_tokens: int = 500
    client: Any = field(default=None, repr=False, compare=False)

    def __post_init__(self) -> None:
        if not self.model_id.strip() or not self.instructions.strip() or not self.region.strip():
            raise ValueError("Bedrock model ID, instructions, and AWS region are required")
        if not 100 <= self.max_output_tokens <= 2_000:
            raise ValueError("max_output_tokens must be between 100 and 2000")

    def _runtime(self) -> Any:
        if self.client is not None:
            return self.client
        try:
            import boto3
            from botocore.config import Config
        except ImportError as error:
            raise ToolError("bedrock_unavailable", "Install shinyhub-agent[bedrock] to use Amazon Bedrock.") from error
        return boto3.client(
            "bedrock-runtime", region_name=self.region,
            config=Config(connect_timeout=5, read_timeout=45, retries={"max_attempts": 2}),
        )

    async def _events(self, client: Any, request: dict[str, Any]) -> AsyncIterator[dict[str, Any]]:
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
            raise ToolError("invalid_message", "Enter a question of at most 2,000 characters.")
        del thread_id  # Bedrock receives bounded history; Shiny owns the viewer's thread.
        messages: list[dict[str, Any]] = [
            {"role": item["role"], "content": [{"text": item["content"][:2_000]}]}
            for item in history[-12:]
            if item.get("role") in ("user", "assistant") and isinstance(item.get("content"), str)
        ]
        messages.append({"role": "user", "content": [{"text": message.strip()}]})
        tool_specs = [{"toolSpec": {
            "name": spec["name"], "description": spec["description"],
            "inputSchema": {"json": spec["inputSchema"]},
        }} for spec in tools.public_spec()["tools"]]
        client = self._runtime()

        for _ in range(4):
            request = {
                "modelId": self.model_id,
                "system": [{"text": self.instructions}],
                "messages": messages,
                "inferenceConfig": {"maxTokens": self.max_output_tokens},
                "toolConfig": {"tools": tool_specs},
            }
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
                        raise ToolError("model_failed", "The assistant sent an invalid response.")
                elif "contentBlockStart" in event:
                    part = event["contentBlockStart"]
                    index = part.get("contentBlockIndex")
                    use = part.get("start", {}).get("toolUse")
                    if not isinstance(index, int) or not isinstance(use, dict):
                        raise ToolError("model_failed", "The assistant sent an invalid tool call.")
                    tool_id, name = use.get("toolUseId"), use.get("name")
                    if not isinstance(tool_id, str) or not 1 <= len(tool_id) <= 128 or not isinstance(name, str):
                        raise ToolError("model_failed", "The assistant sent an invalid tool call.")
                    blocks[index] = {"toolUse": {"toolUseId": tool_id, "name": name}}
                    argument_chunks[index] = ""
                    yield {"type": "status", "text": "Using app tools"}
                elif "contentBlockDelta" in event:
                    part = event["contentBlockDelta"]
                    index = part.get("contentBlockIndex")
                    delta = part.get("delta", {})
                    if not isinstance(index, int) or not isinstance(delta, dict):
                        raise ToolError("model_failed", "The assistant sent an invalid response.")
                    if isinstance(delta.get("text"), str) and delta["text"]:
                        text_seen = True
                        block = blocks.setdefault(index, {"text": ""})
                        if "text" not in block:
                            raise ToolError("model_failed", "The assistant sent an invalid response.")
                        block["text"] += delta["text"]
                        yield {"type": "delta", "text": delta["text"]}
                    elif isinstance(delta.get("toolUse"), dict):
                        chunk = delta["toolUse"].get("input")
                        if index not in argument_chunks or not isinstance(chunk, str):
                            raise ToolError("model_failed", "The assistant sent invalid tool arguments.")
                        argument_chunks[index] += chunk
                        if len(argument_chunks[index].encode()) > 8_192:
                            raise ToolError("model_too_large", "The assistant sent too much tool data.")
                elif "messageStop" in event:
                    stop_reason = event["messageStop"].get("stopReason")
                elif "metadata" in event:
                    usage = event["metadata"].get("usage", {})
                    if usage:
                        logger.info("Bedrock agent usage: input_tokens=%s output_tokens=%s",
                                    usage.get("inputTokens"), usage.get("outputTokens"))
                elif any(key.endswith("Exception") for key in event):
                    raise ToolError("model_failed", "The assistant could not finish its reply.")

            for index, raw in argument_chunks.items():
                try:
                    arguments = json.loads(raw or "{}")
                except ValueError as error:
                    raise ToolError("model_failed", "The assistant sent invalid tool arguments.") from error
                blocks[index]["toolUse"]["input"] = arguments
            content = [blocks[index] for index in sorted(blocks)]
            calls = [part["toolUse"] for part in content if "toolUse" in part]
            if not calls:
                if stop_reason not in ("end_turn", "stop_sequence") or not text_seen:
                    raise ToolError("model_failed", "The assistant did not finish its reply.")
                return
            if stop_reason != "tool_use" or len(calls) > 2:
                raise ToolError("model_failed", "The assistant requested too many or incomplete actions.")
            messages.append({"role": "assistant", "content": content})
            results = []
            for call in calls:
                name = call["name"]
                tool = tools.get(name)
                if tool is None:
                    result: Any = {"error": "Tool unavailable"}
                    succeeded = False
                else:
                    yield {"type": "tool_started", "name": tool.name,
                           "description": tool.description, "readOnly": tool.read_only}
                    succeeded = True
                    try:
                        if not isinstance(call["input"], dict):
                            raise ToolError("invalid_arguments", "Tool arguments must be an object.")
                        if tool.read_only:
                            result = await tools.execute(name, call["input"])
                        else:
                            result = await approve(name, call["input"])
                            yield {"type": "action_applied", "name": name, "result": result}
                    except (ValueError, ToolError) as error:
                        result = {"error": str(error)}
                        succeeded = False
                    yield {"type": "tool_finished", "name": tool.name, "ok": succeeded}
                results.append({"toolResult": {
                    "toolUseId": call["toolUseId"], "content": [{"json": result}],
                    "status": "success" if succeeded else "error",
                }})
            messages.append({"role": "user", "content": results})
            yield {"type": "status", "text": "Writing answer"}
        raise ToolError("model_failed", "The assistant did not finish its reply.")
