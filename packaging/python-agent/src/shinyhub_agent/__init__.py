"""Explicit, session-scoped tools for app assistants and browser agents."""

from ._agui import AGUIChat
from ._bedrock import BedrockChat
from ._charts import ChartData
from ._chat import ChatAgent
from ._core import AgentTool, ToolError, ToolRegistry
from ._openai import OpenAIChat
from ._shiny import agent_dependency, chat_dependency, register
from ._usage import UsageRecord

# Protocol-oriented public name; retain the original class identity and API.
ResponsesChat = OpenAIChat

__version__ = "0.5.3"
__all__ = [
    "AGUIChat",
    "AgentTool",
    "BedrockChat",
    "ChartData",
    "ChatAgent",
    "OpenAIChat",
    "ResponsesChat",
    "ToolError",
    "ToolRegistry",
    "UsageRecord",
    "agent_dependency",
    "chat_dependency",
    "register",
]
