"""Per-call usage records, independent of model prompt and request headers."""

from __future__ import annotations

import asyncio
import inspect
import json
import logging
import secrets
import time
from collections.abc import Callable, Mapping
from contextlib import nullcontext
from contextvars import ContextVar
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from types import MappingProxyType
from typing import Any

logger = logging.getLogger(__name__)
usage_context: ContextVar[Mapping[str, str | None]] = ContextVar(
    "agent_usage_context", default=MappingProxyType({})
)


@dataclass(frozen=True)
class UsageRecord:
    timestamp: str
    provider: str
    app: str | None
    username: str | None
    model: str
    turn_id: str
    call_id: str
    round: int
    final: bool
    input_tokens: int | None
    output_tokens: int | None
    cache_read_tokens: int | None
    cache_write_tokens: int | None
    duration_ms: int
    tool_names: tuple[str, ...]
    outcome: str


UsageCallback = Callable[[UsageRecord], Any]


def token_count(value: Any) -> int | None:
    return (
        value
        if isinstance(value, int) and not isinstance(value, bool) and value >= 0
        else None
    )


class ModelCall:
    def __init__(
        self,
        provider: str,
        model: str,
        turn_id: str,
        round: int,
        final: bool,
        callback: UsageCallback | None,
    ):
        self.provider, self.model = provider, model
        self.turn_id, self.round, self.final = turn_id, round, final
        self.callback = callback
        self.tokens: dict[str, Any] = {}
        self.tool_names: tuple[str, ...] = ()

    async def __aenter__(self):
        self.started = time.monotonic()
        self.timestamp = datetime.now(timezone.utc).isoformat()
        self.context = dict(usage_context.get())
        self.span_context = nullcontext(None)
        try:
            from opentelemetry import trace

            self.span_context = trace.get_tracer(
                "shinyhub_agent"
            ).start_as_current_span("shinyhub.agent.model_call")
            self.span = self.span_context.__enter__()
        except Exception:  # noqa: BLE001 - optional tracing must not break model calls
            self.span_context = nullcontext(None)
            self.span = None
        return self

    async def __aexit__(self, exc_type, exc, tb):
        outcome = (
            "completed"
            if exc_type is None
            else (
                "cancelled" if issubclass(exc_type, asyncio.CancelledError) else "error"
            )
        )
        record = UsageRecord(
            timestamp=self.timestamp,
            provider=self.provider,
            app=self.context.get("app"),
            username=self.context.get("username"),
            model=self.model,
            turn_id=self.context.get("turn_id") or self.turn_id,
            call_id=secrets.token_urlsafe(16),
            round=self.round,
            final=self.final,
            input_tokens=token_count(self.tokens.get("input_tokens")),
            output_tokens=token_count(self.tokens.get("output_tokens")),
            cache_read_tokens=token_count(self.tokens.get("cache_read_tokens")),
            cache_write_tokens=token_count(self.tokens.get("cache_write_tokens")),
            duration_ms=round((time.monotonic() - self.started) * 1_000),
            tool_names=self.tool_names,
            outcome=outcome,
        )
        data = asdict(record)
        try:
            if self.span is not None:
                for name, value in data.items():
                    if value is not None:
                        self.span.set_attribute("shinyhub.agent." + name, value)
        except Exception:  # noqa: BLE001 - optional tracing must not break model calls
            logger.warning("Agent usage span attributes failed")
        try:
            # Exceptions may contain provider request details. Record the outcome only.
            self.span_context.__exit__(None, None, None)
        except Exception:  # noqa: BLE001 - optional tracing must not break model calls
            logger.warning("Agent usage span could not finish")
        logger.info(
            "Agent model usage: %s",
            json.dumps(data, separators=(",", ":")),
            extra={"agent_usage": data},
        )
        if self.callback is not None:
            try:
                result = self.callback(record)
                if inspect.isawaitable(result):
                    await asyncio.wait_for(result, 2.0)
            except asyncio.CancelledError:
                raise
            except Exception:  # noqa: BLE001 - accounting failures must not break answers
                logger.warning("Agent usage callback failed")
        return False
