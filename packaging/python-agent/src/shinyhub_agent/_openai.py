from __future__ import annotations

import json
import logging
from collections.abc import AsyncIterator, Awaitable, Callable, Sequence
from dataclasses import dataclass, field
from typing import Any

import httpx

from ._core import ToolError, ToolRegistry
from ._formatting import panel_instructions

Approve = Callable[[str, dict[str, Any]], Awaitable[Any]]
logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class OpenAIChat:
    """Server-side Responses API agent using the app's registered tools."""

    api_key: str = field(repr=False)
    instructions: str
    model: str = "gpt-4.1-mini"
    max_output_tokens: int = 500

    def __post_init__(self) -> None:
        if not self.api_key.strip():
            raise ValueError("An OpenAI API key is required")
        if not self.instructions.strip():
            raise ValueError("App-specific agent instructions are required")
        if not 100 <= self.max_output_tokens <= 2_000:
            raise ValueError("max_output_tokens must be between 100 and 2000")

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
        inputs: list[dict[str, Any]] = [
            {"role": item["role"], "content": item["content"][:2_000]}
            for item in history[-12:]
            if item.get("role") in ("user", "assistant") and isinstance(item.get("content"), str)
        ]
        inputs.append({"role": "user", "content": message.strip()})
        model_tools = [
            {
                "type": "function",
                "name": item.name,
                "description": item.description,
                "parameters": dict(item.input_schema),
                "strict": False,
            }
            for item in (tools.get(spec["name"]) for spec in tools.public_spec()["tools"])
            if item is not None
        ]
        headers = {"Authorization": f"Bearer {self.api_key}", "Content-Type": "application/json"}
        async with httpx.AsyncClient(timeout=httpx.Timeout(45, connect=5)) as client:
            for _ in range(4):
                output: list[dict[str, Any]] | None = None
                text_seen = False
                total_bytes = 0
                async with client.stream(
                    "POST", "https://api.openai.com/v1/responses",
                    headers=headers,
                    json={
                        "model": self.model,
                        "instructions": panel_instructions(self.instructions),
                        "input": inputs,
                        "tools": model_tools,
                        "parallel_tool_calls": False,
                        "max_output_tokens": self.max_output_tokens,
                        "store": False,
                        "stream": True,
                    },
                ) as response:
                    response.raise_for_status()
                    async for line in response.aiter_lines():
                        total_bytes += len(line.encode())
                        if total_bytes > 1_048_576 or len(line.encode()) > 65_536:
                            raise ToolError("model_too_large", "The assistant sent too much data.")
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
                        elif kind == "response.output_item.added" and event.get("item", {}).get("type") == "function_call":
                            yield {"type": "status", "text": "Using app tools"}
                        elif kind == "response.completed":
                            output = event.get("response", {}).get("output", [])
                            usage = event.get("response", {}).get("usage") or {}
                            if usage:
                                logger.info(
                                    "OpenAI agent usage: input_tokens=%s output_tokens=%s",
                                    usage.get("input_tokens"), usage.get("output_tokens"),
                                )
                        elif kind in ("response.failed", "error"):
                            raise ToolError("model_failed", "The assistant could not finish its reply.")
                if output is None:
                    raise ToolError("model_failed", "The assistant connection ended early.")
                calls = [item for item in output if item.get("type") == "function_call"]
                if not calls:
                    if not text_seen:
                        answer = "\n".join(
                            part["text"] for item in output if item.get("type") == "message"
                            for part in item.get("content", []) if part.get("type") == "output_text"
                            and isinstance(part.get("text"), str)
                        ).strip()
                        if not answer:
                            raise ToolError("model_failed", "The assistant returned no answer.")
                        yield {"type": "delta", "text": answer}
                    return
                if len(calls) > 2:
                    raise ToolError("model_failed", "The assistant requested too many actions.")
                inputs.extend(output)
                for call in calls:
                    name = call.get("name")
                    tool = tools.get(name)
                    if tool is None:
                        result = {"error": "Tool unavailable"}
                    else:
                        yield {"type": "tool_started", "name": tool.name,
                               "description": tool.description, "readOnly": tool.read_only}
                        succeeded = True
                        try:
                            arguments = json.loads(call.get("arguments", ""))
                            if tool.read_only:
                                result = await tools.execute(name, arguments)
                            else:
                                result = await approve(name, arguments)
                                yield {"type": "action_applied", "name": name, "result": result}
                        except (ValueError, ToolError) as error:
                            result = {"error": str(error)}
                            succeeded = False
                        yield {"type": "tool_finished", "name": tool.name, "ok": succeeded}
                    inputs.append({
                        "type": "function_call_output",
                        "call_id": call["call_id"],
                        "output": json.dumps(result),
                    })
                yield {"type": "status", "text": "Writing answer"}
        raise ToolError("model_failed", "The assistant did not finish its reply.")
