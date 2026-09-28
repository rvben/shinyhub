"""Explicit, session-scoped tools for app assistants and browser agents."""

from ._core import AgentTool, ToolError, ToolRegistry
from ._openai import OpenAIChat
from ._agui import AGUIChat
from ._chat import ChatAgent
from ._shiny import agent_dependency, chat_dependency, register

__version__ = "0.1.0"
__all__ = ["AGUIChat", "AgentTool", "ChatAgent", "OpenAIChat", "ToolError", "ToolRegistry", "agent_dependency", "chat_dependency", "register"]
