from __future__ import annotations

import asyncio
import logging
import re
import secrets
import time
from collections import deque
from typing import Any

from ._core import ToolError, ToolRegistry

VERSION = 1
REQUEST_ID = re.compile(r"^[a-zA-Z0-9_-]{8,80}$")
logger = logging.getLogger(__name__)


class SessionTools:
    """Dispatch validated tool calls for exactly one connected viewer."""

    def __init__(self, registry: ToolRegistry):
        self.registry = registry
        self.nonce = secrets.token_urlsafe(18)
        self.lock = asyncio.Lock()
        self.calls: deque[float] = deque()

    def capabilities(self) -> dict[str, Any]:
        return {**self.registry.public_spec(), "session": self.nonce}

    async def handle(self, raw: Any) -> dict[str, Any] | None:
        if not isinstance(raw, dict):
            return None
        request_id = raw.get("requestId")
        if not isinstance(request_id, str) or not REQUEST_ID.fullmatch(request_id):
            return None
        response: dict[str, Any] = {"version": VERSION, "session": self.nonce, "requestId": request_id}
        try:
            if raw.get("version") != VERSION or raw.get("session") != self.nonce:
                raise ToolError("stale_session", "The app session changed. Try again.")
            now = time.monotonic()
            while self.calls and now - self.calls[0] > 60:
                self.calls.popleft()
            if len(self.calls) >= 20:
                raise ToolError("rate_limited", "Too many actions. Try again in a minute.")
            if self.lock.locked():
                raise ToolError("busy", "Another app action is still running.")
            self.calls.append(now)
            async with self.lock:
                response["result"] = await self.registry.execute(raw.get("name"), raw.get("arguments"))
                response["ok"] = True
                logger.info("Agent tool completed: %s", raw.get("name"))
        except ToolError as error:
            response.update(ok=False, code=error.code, message=str(error))
            logger.info("Agent tool rejected: %s (%s)", raw.get("name"), error.code)
        except Exception:
            response.update(ok=False, code="tool_failed", message="The app could not finish this action.")
            logger.exception("Agent tool dispatch failed")
        return response
