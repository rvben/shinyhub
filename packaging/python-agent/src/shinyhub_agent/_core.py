from __future__ import annotations

import asyncio
import copy
import inspect
import json
import re
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from typing import Any

from jsonschema import Draft202012Validator

MAX_ARGUMENT_BYTES = 8_192
MAX_RESULT_BYTES = 32_768
NAME = re.compile(r"^[a-z][a-z0-9_]{0,63}$")


class ToolError(Exception):
    """An expected, safe error that can be shown to the visitor."""

    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code


def _reject_references(value: Any) -> None:
    if isinstance(value, Mapping):
        if "$ref" in value or "$dynamicRef" in value:
            raise ValueError("Tool schemas cannot contain references")
        for nested in value.values():
            _reject_references(nested)
    elif isinstance(value, list):
        for nested in value:
            _reject_references(nested)


@dataclass(frozen=True)
class AgentTool:
    name: str
    description: str
    input_schema: Mapping[str, Any]
    handler: Callable[[dict[str, Any]], Any]
    read_only: bool = True
    confirmation: str = ""

    def __post_init__(self) -> None:
        if not NAME.fullmatch(self.name):
            raise ValueError("Tool names must use lower-case letters, digits and underscores")
        if not isinstance(self.description, str) or not 1 <= len(self.description.strip()) <= 500:
            raise ValueError("Tool descriptions must contain 1 to 500 characters")
        if not isinstance(self.input_schema, Mapping) or self.input_schema.get("type") != "object":
            raise ValueError("A tool input schema must describe an object")
        if self.input_schema.get("additionalProperties") is not False:
            raise ValueError("Tool input schemas must set additionalProperties to false")
        _reject_references(self.input_schema)
        Draft202012Validator.check_schema(self.input_schema)
        object.__setattr__(self, "input_schema", copy.deepcopy(dict(self.input_schema)))
        if len(json.dumps(self.input_schema, allow_nan=False).encode()) > MAX_ARGUMENT_BYTES:
            raise ValueError("Tool input schema is too large")
        if not callable(self.handler):
            raise TypeError("Tool handler must be callable")
        if not inspect.iscoroutinefunction(self.handler):
            raise TypeError("Tool handler must be an async function")
        if not self.read_only and not self.confirmation.strip():
            raise ValueError("State-changing tools need visitor-facing confirmation text")
        if len(self.confirmation) > 300:
            raise ValueError("Confirmation text is too long")

    def public_spec(self) -> dict[str, Any]:
        return {
            "name": self.name,
            "description": self.description.strip(),
            "inputSchema": dict(self.input_schema),
            "readOnly": self.read_only,
            "confirmation": self.confirmation,
        }


class ToolRegistry:
    """An allowlisted set of tools bound to one Shiny session."""

    def __init__(self, tools: Sequence[AgentTool]):
        if not tools or len(tools) > 32:
            raise ValueError("Register between 1 and 32 agent tools")
        names = [tool.name for tool in tools]
        if len(names) != len(set(names)):
            raise ValueError("Agent tool names must be unique")
        self._tools = {tool.name: tool for tool in tools}

    def public_spec(self) -> dict[str, Any]:
        return {"version": 1, "tools": [tool.public_spec() for tool in self._tools.values()]}

    def get(self, name: str) -> AgentTool | None:
        return self._tools.get(name)

    async def execute(self, name: str, arguments: Any, *, timeout: float = 8.0) -> Any:
        tool = self._tools.get(name)
        if tool is None:
            raise ToolError("unknown_tool", "This app does not offer that action.")
        if not isinstance(arguments, dict):
            raise ToolError("invalid_arguments", "Tool arguments must be an object.")
        try:
            encoded = json.dumps(arguments, allow_nan=False)
        except (TypeError, ValueError) as error:
            raise ToolError("invalid_arguments", "Tool arguments must be JSON values.") from error
        if len(encoded.encode()) > MAX_ARGUMENT_BYTES:
            raise ToolError("invalid_arguments", "Tool arguments are too large.")
        if not Draft202012Validator(tool.input_schema).is_valid(arguments):
            raise ToolError("invalid_arguments", "The requested action has invalid arguments.")
        try:
            result = await asyncio.wait_for(tool.handler(arguments), timeout)
            encoded_result = json.dumps(result, allow_nan=False)
        except asyncio.TimeoutError as error:
            raise ToolError("timeout", "The app took too long to finish this action.") from error
        except ToolError:
            raise
        except Exception as error:
            raise ToolError("tool_failed", "The app could not finish this action.") from error
        if len(encoded_result.encode()) > MAX_RESULT_BYTES:
            raise ToolError("result_too_large", "The app returned too much data.")
        return result
