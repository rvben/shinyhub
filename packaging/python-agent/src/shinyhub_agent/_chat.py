from __future__ import annotations

import asyncio
import logging
import secrets
import time
from collections import deque
from collections.abc import Awaitable, Callable
from typing import Any, Protocol

import httpx

from ._core import ToolError, ToolRegistry
from ._session import REQUEST_ID, VERSION

logger = logging.getLogger(__name__)
Send = Callable[[dict[str, Any]], Awaitable[None]]


class ChatAgent(Protocol):
    def run(self, message: str, history: list[dict[str, str]], tools: ToolRegistry,
            approve: Callable[[str, dict[str, Any]], Awaitable[Any]], *, thread_id: str): ...


class ChatSession:
    """One viewer's bounded conversation and write-action approval flow."""

    def __init__(self, agent: ChatAgent, tools: ToolRegistry, send: Send):
        self.agent = agent
        self.tools = tools
        self.send = send
        self.nonce = secrets.token_urlsafe(18)
        self.thread_id = secrets.token_urlsafe(18)
        self.history: list[dict[str, str]] = []
        self.calls: deque[float] = deque()
        self.total_calls = 0
        self.seen_requests: deque[str] = deque(maxlen=32)
        self.lock = asyncio.Lock()
        self.pending_approval: tuple[str, asyncio.Future[bool]] | None = None
        self.last_action: tuple[str, str, dict[str, Any], Any, float] | None = None
        self.task: asyncio.Task[Any] | None = None

    def capabilities(self) -> dict[str, Any]:
        return {"version": VERSION, "session": self.nonce, "enabled": True}

    async def handle(self, raw: Any) -> None:
        if not isinstance(raw, dict):
            return
        request_id = raw.get("requestId")
        if not isinstance(request_id, str) or not REQUEST_ID.fullmatch(request_id):
            return

        async def emit(event: dict[str, Any]) -> None:
            await self.send({"version": VERSION, "session": self.nonce,
                             "requestId": request_id, **event})

        if raw.get("version") != VERSION or raw.get("session") != self.nonce:
            await emit({"type": "error", "message": "The app session changed. Start a new chat."})
            return
        if raw.get("action") == "reset":
            if not self.lock.locked():
                self.history.clear()
                self.thread_id = secrets.token_urlsafe(18)
                self.last_action = None
                await emit({"type": "reset"})
            return
        if raw.get("action") == "cancel":
            if self.task is not None:
                self.task.cancel()
            return
        if raw.get("action") == "undo":
            if self.lock.locked():
                await emit({"type": "undo_result", "ok": False,
                            "message": "Wait for the answer to finish, then try Undo."})
                return
            async with self.lock:
                action = self.last_action
                if (action is None or raw.get("actionId") != action[0]
                        or time.monotonic() - action[4] > 300):
                    await emit({"type": "undo_result", "ok": False,
                                "message": "This change can no longer be undone."})
                    return
                tool = self.tools.get(action[1])
                if tool is None or tool.undo is None:
                    await emit({"type": "undo_result", "ok": False,
                                "message": "Undo is unavailable for this change."})
                    return
                self.last_action = None
                try:
                    result = await asyncio.wait_for(tool.undo(action[2], action[3]), 8.0)
                    self.history.clear()
                    self.thread_id = secrets.token_urlsafe(18)
                    detail = self._receipt(tool, action[2], result, "Previous view restored")
                    await emit({"type": "undo_result", "ok": True,
                                "actionId": action[0], "detail": detail})
                except Exception:
                    logger.exception("Agent chat undo failed")
                    await emit({"type": "undo_result", "ok": False,
                                "actionId": action[0],
                                "message": "Undo could not finish. Check the current view."})
            return
        if request_id in self.seen_requests:
            await emit({"type": "error", "message": "That question was already submitted."})
            return
        message = raw.get("message")
        if not isinstance(message, str) or not 1 <= len(message.strip()) <= 2_000:
            await emit({"type": "error", "message": "Enter a question of at most 2,000 characters."})
            return
        now = time.monotonic()
        while self.calls and now - self.calls[0] > 60:
            self.calls.popleft()
        if len(self.calls) >= 10:
            await emit({"type": "error", "message": "Too many questions. Try again in a minute."})
            return
        if self.total_calls >= 40:
            await emit({"type": "error", "message": "This chat reached its question limit. Reopen the app to start a new session."})
            return
        if self.lock.locked():
            await emit({"type": "error", "message": "The assistant is still answering."})
            return
        self.calls.append(now)
        self.total_calls += 1
        self.seen_requests.append(request_id)
        answer = ""
        applied_actions: list[str] = []

        def remember_applied_actions() -> None:
            if not applied_actions:
                return
            self.history.extend([
                {"role": "user", "content": message.strip()},
                {"role": "assistant", "content": "Applied app action: " + ", ".join(applied_actions) + ". The reply was interrupted."},
            ])
            self.history = self.history[-12:]

        async with self.lock:
            self.task = asyncio.current_task()
            try:
                await emit({"type": "status", "text": "Reading your question"})
                async for event in self.agent.run(
                    message.strip(), self.history, self.tools, self.approve,
                    thread_id=self.thread_id,
                ):
                    if event.get("type") == "delta":
                        answer += event.get("text", "")
                        if len(answer) > 8_000:
                            raise ToolError("answer_too_large", "The answer was too long.")
                    elif event.get("type") == "action_applied":
                        applied_actions.append(str(event.get("name", "app action")))
                        action = self.last_action
                        if action is not None and event.get("name") == action[1]:
                            tool = self.tools.get(action[1])
                            event = {**event, "receipt": self._receipt(
                                tool, action[2], action[3], "Change applied to this view")}
                            if tool and tool.undo is not None:
                                event["actionId"] = action[0]
                    await emit(event)
                if not answer.strip():
                    raise ToolError("empty_answer", "The assistant returned no answer.")
                self.history.extend([
                    {"role": "user", "content": message.strip()},
                    {"role": "assistant", "content": answer},
                ])
                self.history = self.history[-12:]
                await emit({"type": "done"})
                logger.info("Agent chat completed")
            except asyncio.CancelledError:
                remember_applied_actions()
                await emit({"type": "error", "message": "Stopped. The incomplete answer was discarded."})
            except (ToolError, httpx.HTTPError, ValueError, KeyError, TypeError):
                logger.exception("Agent chat run failed")
                remember_applied_actions()
                await emit({"type": "error", "message": "The assistant could not finish. Try again."})
            except Exception:
                logger.exception("Unexpected agent chat failure")
                remember_applied_actions()
                await emit({"type": "error", "message": "The assistant could not finish. Try again."})
            finally:
                self.task = None
                if self.pending_approval is not None:
                    _, future = self.pending_approval
                    if not future.done():
                        future.cancel()
                    self.pending_approval = None

    async def approve(self, name: str, arguments: dict[str, Any]) -> Any:
        tool = self.tools.get(name)
        if tool is None or tool.read_only:
            raise ToolError("invalid_action", "The requested action is unavailable.")
        approval_id = secrets.token_urlsafe(16)
        future: asyncio.Future[bool] = asyncio.get_running_loop().create_future()
        self.pending_approval = (approval_id, future)
        await self.send({
            "version": VERSION, "session": self.nonce, "type": "approval_required",
            "approvalId": approval_id, "name": name, "arguments": arguments,
            "message": tool.confirmation,
        })
        try:
            allowed = await asyncio.wait_for(future, 30)
        except asyncio.TimeoutError as error:
            raise ToolError("approval_timeout", "The action was not approved in time.") from error
        finally:
            self.pending_approval = None
        if not allowed:
            raise ToolError("action_declined", "The visitor declined the action.")
        result = await self.tools.execute(name, arguments)
        self.last_action = (secrets.token_urlsafe(16), name, arguments, result, time.monotonic())
        return result

    @staticmethod
    def _receipt_text(value: Any) -> str:
        if not isinstance(value, str) or not 1 <= len(value.strip()) <= 140:
            raise ValueError("Receipt text must contain 1 to 140 characters")
        return value.strip()

    @classmethod
    def _receipt(cls, tool: Any, arguments: dict[str, Any], result: Any,
                 fallback: str) -> str:
        if tool is None or tool.receipt is None:
            return fallback
        try:
            return cls._receipt_text(tool.receipt(arguments, result))
        except Exception:
            logger.exception("Agent tool receipt failed")
            return fallback

    def decide(self, raw: Any) -> None:
        if not isinstance(raw, dict) or self.pending_approval is None:
            return
        approval_id, future = self.pending_approval
        if (raw.get("version") == VERSION and raw.get("session") == self.nonce
                and raw.get("approvalId") == approval_id and isinstance(raw.get("approved"), bool)
                and not future.done()):
            future.set_result(raw["approved"])
