"""Explicit, session-scoped tools for app assistants and browser agents."""

from ._core import AgentTool, ToolError, ToolRegistry
from ._openai import OpenAIChat
from ._agui import AGUIChat
from ._bedrock import BedrockChat
from ._chat import ChatAgent
from ._shiny import agent_dependency, chat_dependency, register

__version__ = "0.2.0b2"
__all__ = ["AGUIChat", "AgentTool", "BedrockChat", "ChatAgent", "OpenAIChat", "ToolError", "ToolRegistry", "agent_dependency", "chat_dependency", "register"]
