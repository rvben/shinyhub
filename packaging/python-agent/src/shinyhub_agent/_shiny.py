from __future__ import annotations

import asyncio
from collections.abc import Sequence
from typing import Any

from ._core import AgentTool, ToolRegistry
from ._chat import ChatAgent, ChatSession
from ._session import SessionTools

REQUEST_INPUT = ".shinyhub_agent_request"
DISCOVER_INPUT = ".shinyhub_agent_discover"
CAPABILITIES_MESSAGE = "shinyhub-agent-capabilities"
RESULT_MESSAGE = "shinyhub-agent-result"
CHAT_CAPABILITIES_MESSAGE = "shinyhub-agent-chat-capabilities"
CHAT_EVENT_MESSAGE = "shinyhub-agent-chat-event"
CHAT_INPUT = ".shinyhub_agent_chat_request"
CHAT_DECISION_INPUT = ".shinyhub_agent_chat_decision"
REGISTRATION_MARKER = "_shinyhub_agent_registration"


def agent_dependency():
    """Add to the UI to expose tools to supported browser agents."""
    from htmltools import HTMLDependency

    return HTMLDependency(
        "shinyhub-agent", "0.2.0b4",
        source={"package": "shinyhub_agent", "subdir": "www"},
        script={"src": "bridge.js", "defer": "defer"},
        stylesheet={"href": "bridge.css"},
    )


def chat_dependency():
    """Add the built-in chat panel to the UI when registering a chat agent."""
    from htmltools import HTMLDependency

    return HTMLDependency(
        "shinyhub-agent-chat", "0.2.0b4",
        source={"package": "shinyhub_agent", "subdir": "www"},
        script={"src": "chat.js", "defer": "defer"},
        stylesheet={"href": "chat.css"},
    )


def register(*, session: Any, input: Any, tools: Sequence[AgentTool],
             chat: ChatAgent | None = None, allow_browser_writes: bool = False) -> ToolRegistry:
    """Register session tools; browser writes require an explicit opt-in.

    Chat writes retain server-side approval regardless of this setting.
    """
    from shiny import reactive

    if getattr(session, REGISTRATION_MARKER, None) is not None:
        raise RuntimeError("Register agent tools only once per session")
    namespace_probe = "__shinyhub_namespace_probe"
    if str(session.ns(namespace_probe)) != namespace_probe:
        raise ValueError("Register agent tools from the top-level server session")
    registry = ToolRegistry(tools)
    setattr(session, REGISTRATION_MARKER, registry)
    dispatcher = SessionTools(registry, allow_browser_writes=allow_browser_writes)
    chat_session = ChatSession(
        chat, registry, lambda event: session.send_custom_message(CHAT_EVENT_MESSAGE, event)
    ) if chat is not None else None

    async def publish() -> None:
        await session.send_custom_message(
            CAPABILITIES_MESSAGE,
            dispatcher.capabilities(),
        )
        if chat_session is not None:
            await session.send_custom_message(CHAT_CAPABILITIES_MESSAGE, chat_session.capabilities())

    session.on_flushed(publish, once=True)

    @reactive.effect
    @reactive.event(input[DISCOVER_INPUT], ignore_none=True)
    async def _discover() -> None:
        await publish()

    @reactive.effect
    @reactive.event(input[REQUEST_INPUT], ignore_none=True)
    async def _request() -> None:
        response = await dispatcher.handle(input[REQUEST_INPUT]())
        if response is not None:
            await session.send_custom_message(RESULT_MESSAGE, response)

    if chat_session is not None:
        @reactive.effect
        @reactive.event(input[CHAT_INPUT], ignore_none=True)
        def _chat_request() -> None:
            asyncio.create_task(chat_session.handle(input[CHAT_INPUT]()))

        @reactive.effect
        @reactive.event(input[CHAT_DECISION_INPUT], ignore_none=True)
        def _chat_decision() -> None:
            chat_session.decide(input[CHAT_DECISION_INPUT]())

    return registry
