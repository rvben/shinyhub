import asyncio
from unittest.mock import patch

import pytest
from shinyhub_agent import AgentTool, ToolError, ToolRegistry
from shinyhub_agent._chat import ChatSession
from shinyhub_agent._session import SessionTools

SCHEMA = {
    "type": "object",
    "properties": {"period": {"type": "string", "enum": ["week", "year"]}},
    "required": ["period"],
    "additionalProperties": False,
}


def make_chat(events, applied, **options):
    async def write(args):
        applied.append(args)
        args["period"] = "handler changed its own snapshot"
        return {"applied": True}

    async def send(event):
        events.append(event)

    tool = AgentTool(
        "set_period",
        "Change period",
        SCHEMA,
        write,
        read_only=False,
        confirmation="Change period?",
        **options,
    )
    return ChatSession(None, ToolRegistry([tool]), send)


async def wait_for_card(events):
    async def wait():
        while not any(event["type"] == "approval_required" for event in events):
            await asyncio.sleep(0)

    await asyncio.wait_for(wait(), 1)
    return next(event for event in events if event["type"] == "approval_required")


def decide(chat, card):
    chat.decide(
        {
            "version": 1,
            "session": chat.nonce,
            "approvalId": card["approvalId"],
            "approved": True,
        }
    )


@pytest.mark.parametrize(
    "args", [{"period": "October"}, [], {"period": "week", "extra": True}]
)
def test_schema_invalid_write_never_shows_approval(args):
    events, applied = [], []
    chat = make_chat(events, applied)
    with pytest.raises(ToolError, match="arguments"):
        asyncio.run(chat.approve("set_period", args))
    assert events == applied == []


@pytest.mark.parametrize("async_validator", [False, True])
def test_app_validator_rejects_without_approval_and_preserves_safe_error(
    async_validator,
):
    def validate(args):
        raise ToolError("no_data", "October has no data yet.")

    async def validate_async(args):
        await asyncio.sleep(0)
        validate(args)

    events, applied = [], []
    chat = make_chat(
        events, applied, validate=validate_async if async_validator else validate
    )
    with pytest.raises(ToolError) as error:
        asyncio.run(chat.approve("set_period", {"period": "week"}))
    assert error.value.code == "no_data"
    assert str(error.value) == "October has no data yet."
    assert events == applied == []


def test_validator_rechecks_state_after_approval():
    state = {"available": True}
    events, applied = [], []

    def validate(args):
        if not state["available"]:
            raise ToolError("stale_view", "The available periods changed. Ask again.")

    async def run():
        chat = make_chat(events, applied, validate=validate)
        task = asyncio.create_task(chat.approve("set_period", {"period": "year"}))
        card = await wait_for_card(events)
        state["available"] = False
        decide(chat, card)
        with pytest.raises(ToolError) as error:
            await task
        assert error.value.code == "stale_view"
        assert chat.last_action is None

    asyncio.run(run())
    assert applied == []


def test_approved_snapshot_cannot_be_rewritten_by_caller_validator_or_handler():
    events, applied = [], []

    def validate(args):
        args["period"] = "week"

    async def run():
        chat = make_chat(
            events,
            applied,
            validate=validate,
            describe=lambda args: f"Period = {args['period']}",
        )
        original = {"period": "year"}
        task = asyncio.create_task(chat.approve("set_period", original))
        card = await wait_for_card(events)
        assert card["description"] == "Period = year"
        assert card["expiresIn"] == 30
        original["period"] = "week"
        decide(chat, card)
        await task
        assert card["arguments"] == chat.last_action[2] == {"period": "year"}

    asyncio.run(run())
    assert len(applied) == 1


@pytest.mark.parametrize("description", ["", "a" * 301, None])
def test_invalid_custom_description_falls_back_to_complete_arguments(description):
    events, applied = [], []

    async def run():
        chat = make_chat(events, applied, describe=lambda args: description)
        task = asyncio.create_task(chat.approve("set_period", {"period": "year"}))
        card = await wait_for_card(events)
        assert card["description"] == ""
        assert card["arguments"] == {"period": "year"}
        task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await task

    asyncio.run(run())


def test_approval_timeout_emits_correlated_expiry_and_does_not_execute():
    events, applied = [], []
    real_wait = asyncio.wait_for

    async def expire(future, timeout):
        if isinstance(future, asyncio.Future):
            future.cancel()
            raise asyncio.TimeoutError
        return await real_wait(future, timeout)

    async def run():
        chat = make_chat(events, applied)
        with (
            patch("shinyhub_agent._chat.asyncio.wait_for", side_effect=expire),
            pytest.raises(ToolError) as error,
        ):
            await chat.approve("set_period", {"period": "year"})
        assert error.value.code == "approval_timeout"
        assert events[1]["type"] == "approval_expired"
        assert events[1]["approvalId"] == events[0]["approvalId"]
        assert chat.pending_approval is None
        decide(chat, events[0])  # A late decision cannot revive the action.

    asyncio.run(run())
    assert applied == []


def test_only_one_approval_can_be_pending_even_for_a_custom_parallel_agent():
    events, applied = [], []

    async def run():
        chat = make_chat(events, applied)
        first = asyncio.create_task(chat.approve("set_period", {"period": "year"}))
        card = await wait_for_card(events)
        with pytest.raises(ToolError) as error:
            await chat.approve("set_period", {"period": "week"})
        assert error.value.code == "approval_pending"
        assert len(events) == 1
        decide(chat, card)
        await first

    asyncio.run(run())


def test_browser_preflight_returns_description_without_executing_and_revalidates():
    events, applied = [], []
    available = True

    def validate(args):
        if not available:
            raise ToolError("no_data", "The period is unavailable.")

    chat = make_chat(
        events, applied, validate=validate, describe=lambda args: "Switch to year"
    )
    dispatcher = SessionTools(chat.tools, allow_browser_writes=True)

    async def run():
        nonlocal available
        raw = {
            "version": 1,
            "session": dispatcher.nonce,
            "requestId": "request_12345678",
            "name": "set_period",
            "arguments": {"period": "year"},
            "action": "prepare",
        }
        prepared = await dispatcher.handle(raw)
        assert prepared["result"]["description"] == "Switch to year"
        assert applied == []
        available = False
        rejected = await dispatcher.handle({**raw, "action": "execute"})
        assert rejected["code"] == "no_data"
        assert applied == []

    asyncio.run(run())


@pytest.mark.parametrize(
    "options",
    [
        {"approval_timeout": 0},
        {"approval_timeout": float("nan")},
        {"history_exchanges": True},
        {"max_answer_chars": 100_000},
        {"usage_metadata": {"secret": "do not log"}},
    ],
)
def test_chat_configuration_is_bounded(options):
    async def send(event):
        pass

    with pytest.raises(ValueError):
        ChatSession(None, None, send, **options)
