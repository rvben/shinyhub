import asyncio

import pytest

from shinyhub_agent import AgentTool, ToolError, ToolRegistry


SCHEMA = {
    "type": "object",
    "properties": {"period": {"type": "string", "enum": ["week", "year"]}},
    "required": ["period"],
    "additionalProperties": False,
}


async def noop(_):
    return None


def tool(handler):
    return AgentTool("set_period", "Change period", SCHEMA, handler,
                     read_only=False, confirmation="Change the period?")


def test_validated_action_returns_handler_result():
    seen = []

    async def apply(args):
        seen.append(args)
        return {"period": args["period"]}

    registry = ToolRegistry([tool(apply)])
    result = asyncio.run(registry.execute("set_period", {"period": "year"}))
    assert result == {"period": "year"}
    assert seen == [{"period": "year"}]
    assert registry.public_spec()["tools"][0]["readOnly"] is False


@pytest.mark.parametrize("name,args,code", [
    ("delete_all", {}, "unknown_tool"),
    ("set_period", {"period": "decade"}, "invalid_arguments"),
    ("set_period", {"period": "week", "extra": 1}, "invalid_arguments"),
    ("set_period", [], "invalid_arguments"),
])
def test_invalid_calls_never_reach_handler(name, args, code):
    async def forbidden(_):
        raise AssertionError("handler must not run")

    with pytest.raises(ToolError) as error:
        asyncio.run(ToolRegistry([tool(forbidden)]).execute(name, args))
    assert error.value.code == code


def test_remote_schema_reference_is_rejected():
    with pytest.raises(ValueError, match="references"):
        AgentTool("unsafe", "Unsafe", {
            "type": "object", "properties": {"x": {"$ref": "https://example.com/schema"}},
            "additionalProperties": False,
        }, noop)


def test_write_tool_needs_confirmation_text():
    with pytest.raises(ValueError, match="confirmation"):
        AgentTool("change", "Change", SCHEMA, noop, read_only=False)


def test_exception_is_sanitized():
    async def broken(_):
        raise RuntimeError("private database detail")

    with pytest.raises(ToolError) as error:
        asyncio.run(ToolRegistry([tool(broken)]).execute("set_period", {"period": "week"}))
    assert error.value.code == "tool_failed"
    assert "private" not in str(error.value)


def test_timeout_is_reported():
    async def slow(_):
        await asyncio.sleep(0.1)

    with pytest.raises(ToolError) as error:
        asyncio.run(ToolRegistry([tool(slow)]).execute("set_period", {"period": "week"}, timeout=0.001))
    assert error.value.code == "timeout"
