"""Shared, ordered tool execution and bounded recovery for chat backends."""

from __future__ import annotations

import secrets
from collections.abc import AsyncIterator, Awaitable, Callable, Sequence
from dataclasses import dataclass, field
from typing import Any

from ._core import ToolError, ToolRegistry

Approve = Callable[[str, dict[str, Any]], Awaitable[Any]]
FINAL_INSTRUCTIONS = (
    "The app tool budget is exhausted. Answer briefly using only completed tool results. "
    "Explain missing information or rejected actions. Do not request more tools or claim "
    "that a deferred, declined, or failed action was applied."
)


def bounded_int(name: str, value: int, minimum: int, maximum: int) -> None:
    if (
        isinstance(value, bool)
        or not isinstance(value, int)
        or not minimum <= value <= maximum
    ):
        raise ValueError(f"{name} must be an integer between {minimum} and {maximum}")


def validate_limits(step: int, rounds: int, turn: int) -> None:
    bounded_int("max_tool_calls_per_step", step, 1, 8)
    bounded_int("max_tool_rounds", rounds, 1, 8)
    bounded_int("max_tool_calls_per_turn", turn, 1, 64)


def bounded_history(history: Sequence[dict[str, str]]) -> list[dict[str, str]]:
    return [
        {"role": item["role"], "content": item["content"][:32_768]}
        for item in history[-64:]
        if item.get("role") in ("user", "assistant")
        and isinstance(item.get("content"), str)
    ]


@dataclass(frozen=True)
class ToolCall:
    id: str
    name: str
    arguments: Any


def check_calls(calls: Sequence[ToolCall]) -> None:
    # Protocol integrity is separate from the configurable execution budget.
    if len(calls) > 64:
        raise ToolError("model_failed", "The assistant sent invalid tool calls.")
    if any(
        not isinstance(call.id, str)
        or not 1 <= len(call.id) <= 128
        or not isinstance(call.name, str)
        or not 1 <= len(call.name) <= 64
        for call in calls
    ):
        raise ToolError("model_failed", "The assistant sent an invalid tool call.")
    if len({call.id for call in calls}) != len(calls):
        raise ToolError(
            "model_failed", "The assistant repeated a tool call identifier."
        )


@dataclass
class Turn:
    max_step: int
    max_calls: int
    id: str = field(default_factory=lambda: secrets.token_urlsafe(16))
    calls: int = 0
    reads: list[str] = field(default_factory=list)
    writes: list[str] = field(default_factory=list)
    errors: list[str] = field(default_factory=list)

    @property
    def exhausted(self) -> bool:
        return self.calls >= self.max_calls

    def fallback(self) -> str:
        parts = [
            "I reached the assistant's action limit and could not finish the answer."
        ]
        if self.writes:
            parts.append(
                "Applied actions: " + ", ".join(dict.fromkeys(self.writes)) + "."
            )
        if self.reads:
            parts.append(
                "Completed reads: " + ", ".join(dict.fromkeys(self.reads)) + "."
            )
        if self.errors:
            parts.append(" ".join(list(dict.fromkeys(self.errors))[-2:]))
        parts.append("Ask a narrower question to continue.")
        return "\n\n".join(parts)

    async def execute(
        self, calls: Sequence[ToolCall], tools: ToolRegistry, approve: Approve
    ) -> AsyncIterator[dict[str, Any]]:
        check_calls(calls)
        writes = 0
        blocked = False
        for index, call in enumerate(calls):
            tool = tools.get(call.name)
            if (
                blocked
                or index >= self.max_step
                or self.exhausted
                or (tool is not None and not tool.read_only and writes >= 1)
            ):
                # Defer the remaining suffix, so a later read cannot pass a deferred write.
                blocked = True
                result = {
                    "code": "tool_deferred",
                    "error": "Action deferred by the app tool budget (at most "
                    f"{self.max_step} calls and one write per step, {self.max_calls} calls "
                    "per turn). Call again in a later step only if still needed.",
                }
                yield {
                    "type": "tool_result",
                    "id": call.id,
                    "result": result,
                    "ok": False,
                }
                continue
            self.calls += 1
            if tool is None:
                result = {
                    "code": "unknown_tool",
                    "error": "This app does not offer that action.",
                }
                self.errors.append(result["error"])
                yield {
                    "type": "tool_result",
                    "id": call.id,
                    "result": result,
                    "ok": False,
                }
                continue
            yield {
                "type": "tool_started",
                "name": tool.name,
                "description": tool.description,
                "readOnly": tool.read_only,
            }
            succeeded = False
            try:
                if tool.read_only:
                    result = await tools.execute(call.name, call.arguments)
                    self.reads.append(call.name)
                else:
                    writes += 1
                    result = await approve(call.name, call.arguments)
                    self.writes.append(call.name)
                    yield {
                        "type": "action_applied",
                        "name": call.name,
                        "result": result,
                    }
                succeeded = True
            except ToolError as error:
                result = {"code": error.code, "error": str(error)}
                self.errors.append(str(error)[:300])
            except (ValueError, TypeError):
                result = {
                    "code": "invalid_arguments",
                    "error": "The requested action has invalid arguments.",
                }
                self.errors.append(result["error"])
            yield {
                "type": "tool_finished",
                "name": tool.name,
                "ok": succeeded,
                **({"code": result["code"]} if not succeeded else {}),
            }
            yield {
                "type": "tool_result",
                "id": call.id,
                "result": result,
                "ok": succeeded,
            }
