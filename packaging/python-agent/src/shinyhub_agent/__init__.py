"""Explicit, session-scoped tools for app assistants and browser agents."""

from ._agui import AGUIChat
from ._bedrock import BedrockChat
from ._chat import ChatAgent
from ._core import AgentTool, ToolError, ToolRegistry
from ._openai import OpenAIChat
from ._shiny import agent_dependency, chat_dependency, register
from ._usage import UsageRecord

__version__ = "0.2.0"
__all__ = [
    "AGUIChat",
    "AgentTool",
    "BedrockChat",
    "ChatAgent",
    "OpenAIChat",
    "ToolError",
    "ToolRegistry",
    "UsageRecord",
    "agent_dependency",
    "chat_dependency",
    "register",
]
