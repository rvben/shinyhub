from __future__ import annotations

import asyncio
import json
import logging
from collections.abc import AsyncIterator, Callable, Sequence
from copy import deepcopy
from dataclasses import dataclass, field, replace
from typing import Any

from ._core import ToolError, ToolRegistry
from ._errors import ProviderError
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
from ._suggestions import (
    FOLLOWUP_TOOL,
    RECOVERY_INSTRUCTIONS,
    RECOVERY_RESULT,
    check_recovery,
    followup_spec,
    only_followups,
    recovery_followups,
    terminal_followups,
)
from ._usage import ModelCall, UsageCallback

logger = logging.getLogger(__name__)

_END = object()


_PERMANENT_PROVIDER_ERRORS = (
    "ValidationException",
    "AccessDeniedException",
    "ResourceNotFoundException",
)
_LOGGABLE_PROVIDER_ERRORS = (
    *_PERMANENT_PROVIDER_ERRORS,
    "InternalServerException",
    "ModelErrorException",
    "ModelNotReadyException",
    "ModelTimeoutException",
    "ServiceUnavailableException",
    "ThrottlingException",
)


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

    followups: int = field(default=0, kw_only=True)
    request_policy: Callable[[dict[str, Any]], bool] | None = field(
        default=None, kw_only=True, repr=False, compare=False
    )

    def with_followups(self, maximum: int) -> BedrockChat:
        """Return a session-specific copy; never mutate a shared app adapter."""
        return replace(self, followups=maximum)

    def __post_init__(self) -> None:
        bounded_int("followups", self.followups, 0, 3)
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
        if self.request_policy is not None and not callable(self.request_policy):
            raise TypeError("request_policy must be callable")

    def _check_request_policy(self, request: dict[str, Any]) -> None:
        if self.request_policy is None:
            return
        try:
            allowed = self.request_policy(deepcopy(request))
        except Exception:
            raise ProviderError("model_request_blocked") from None
        if allowed is not True:
            raise ProviderError("model_request_blocked")

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
                if not (self.followups and name == FOLLOWUP_TOOL):
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
                if self.followups and blocks[index]["toolUse"]["name"] == FOLLOWUP_TOOL:
                    arguments = None
                else:
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

    async def _recover(
        self,
        client: Any,
        request: dict[str, Any],
        response: dict[str, Any],
        turn: Turn,
        round_number: int,
    ) -> AsyncIterator[dict[str, Any]]:
        questions = recovery_followups(response["calls"], self.followups)
        # Keep native tool results: Converse requires toolConfig for this transcript
        # and has no portable tool-choice "none". Reject any new app calls below.
        content = [
            {"toolUse": {**part["toolUse"], "input": {"questions": questions}}}
            if "toolUse" in part
            else part
            for part in response["content"]
            if "text" not in part or part["text"].strip()
        ]
        request = {
            **request,
            "system": [
                {
                    "text": panel_instructions(self.instructions)
                    + "\n\n"
                    + RECOVERY_INSTRUCTIONS
                }
            ],
            "messages": [
                *request["messages"],
                {"role": "assistant", "content": content},
                {
                    "role": "user",
                    "content": [
                        {
                            "toolResult": {
                                "toolUseId": call.id,
                                "content": [{"json": RECOVERY_RESULT}],
                                "status": "success",
                            }
                        }
                        for call in response["calls"]
                    ],
                },
            ],
        }
        self._check_request_policy(request)
        yield {"type": "status", "text": "Writing answer"}
        async with ModelCall(
            "bedrock", self.model_id, turn.id, round_number, False, self.on_usage
        ) as usage:
            async for event in self._response(client, request, usage):
                if event["type"] == "model_response":
                    response = event
                elif event["type"] == "delta":
                    yield event
            check_recovery(
                response["calls"],
                any(part.get("text", "").strip() for part in response["content"]),
            )
        if questions:
            yield {"type": "suggestions", "questions": questions}

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
        specs = tools.public_spec()["tools"]
        if self.followups:
            if tools.get(FOLLOWUP_TOOL) is not None:
                raise ValueError(
                    "suggest_followups is reserved when followups are enabled"
                )
            specs = [*specs, followup_spec(self.followups)]
        tool_specs = [
            {
                "toolSpec": {
                    "name": spec["name"],
                    "description": spec["description"],
                    "inputSchema": {"json": spec["inputSchema"]},
                }
            }
            for spec in specs
        ]
        client = self._runtime()
        turn = Turn(self.max_tool_calls_per_step, self.max_tool_calls_per_turn)
        text_streamed = False
        for round_index in range(self.max_tool_rounds + 1):
            final = round_index == self.max_tool_rounds or turn.exhausted
            instructions = panel_instructions(
                self.instructions, followups=bool(self.followups and not final)
            )
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
            # Final output stays buffered until a complete, tool-free answer succeeds.
            # Retrying this request reuses completed results without executing tools.
            for attempt in range(2 if final else 1):
                self._check_request_policy(request)
                buffered = []
                try:
                    async with ModelCall(
                        "bedrock",
                        self.model_id,
                        turn.id,
                        round_index + 1 + attempt,
                        final,
                        self.on_usage,
                    ) as usage:
                        async for event in self._response(client, request, usage):
                            if event["type"] == "model_response":
                                response = event
                            elif final:
                                buffered.append(event)
                            else:
                                if event["type"] == "delta" and event["text"]:
                                    text_streamed = True
                                yield event
                        if final and response["calls"]:
                            raise ToolError(
                                "model_failed",
                                "The assistant requested tools after its budget ended.",
                            )
                except Exception as error:
                    if not final:
                        raise
                    details = getattr(error, "response", None)
                    provider_error = (
                        details.get("Error") if isinstance(details, dict) else None
                    )
                    code = (
                        provider_error.get("Code")
                        if isinstance(provider_error, dict)
                        else None
                    )
                    logger.warning(
                        "Bedrock final answer attempt %d failed (%s), turn %s",
                        attempt + 1,
                        code if code in _LOGGABLE_PROVIDER_ERRORS else type(error).__name__,
                        usage.context.get("turn_id") or turn.id,
                    )
                    # Permanent API errors cannot recover with an identical request.
                    # Model-output failures can recover with a fresh generation.
                    if attempt == 0 and code not in _PERMANENT_PROVIDER_ERRORS:
                        continue
                    prefix = "\n\n" if text_streamed else ""
                    yield {"type": "delta", "text": prefix + turn.fallback()}
                    return
                break
            if final:
                if text_streamed:
                    yield {"type": "delta", "text": "\n\n"}
                for event in buffered:
                    yield event
                return
            if not response["calls"]:
                return
            if self.followups:
                has_text = any(
                    part.get("text", "").strip() for part in response["content"]
                )
                if not has_text and only_followups(response["calls"]):
                    async for event in self._recover(
                        client, request, response, turn, round_index + 2
                    ):
                        yield event
                    return
                questions = terminal_followups(
                    response["calls"],
                    has_text,
                    self.followups,
                )
                response["calls"] = [
                    call for call in response["calls"] if call.name != FOLLOWUP_TOOL
                ]
                response["content"] = [
                    part
                    for part in response["content"]
                    if part.get("toolUse", {}).get("name") != FOLLOWUP_TOOL
                ]
                if not response["calls"]:
                    if not any(
                        part.get("text", "").strip() for part in response["content"]
                    ):
                        raise ToolError(
                            "empty_answer", "The assistant returned no answer."
                        )
                    if questions:
                        yield {"type": "suggestions", "questions": questions}
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
