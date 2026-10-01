import asyncio

import pytest
from shiny import Inputs, reactive
from shiny._namespaces import Root

from shinyhub_agent import AgentTool, register
from shinyhub_agent._shiny import CAPABILITIES_MESSAGE, REQUEST_INPUT, RESULT_MESSAGE


@pytest.mark.parametrize("allow_writes", [False, True])
def test_registered_browser_write_policy_is_enforced_in_shiny_session(monkeypatch, allow_writes):
    from shiny.reactive._core import _reactive_environment
    monkeypatch.setattr(_reactive_environment, "_lock", None)
    effects = []
    effect = reactive.effect

    def track_effect(fn):
        registered = effect(fn, session=None)
        effects.append(registered)
        return registered

    monkeypatch.setattr(reactive, "effect", track_effect)

    async def run():
        class Session:
            ns = Root

            def __init__(self):
                self.messages = []
                self.flushed = []
                self.ended = []
                self.responded = asyncio.Event()

            def on_flushed(self, fn, once):
                self.flushed.append(fn)

            def on_ended(self, fn):
                self.ended.append(fn)

            async def send_custom_message(self, name, payload):
                self.messages.append((name, payload))
                if name == RESULT_MESSAGE:
                    self.responded.set()

        calls = []

        async def write(args):
            calls.append(args)
            return {"changed": True}

        session = Session()
        browser_request = reactive.Value(None)
        options = {"allow_browser_writes": True} if allow_writes else {}
        register(session=session, input=Inputs({REQUEST_INPUT: browser_request}), tools=[
            AgentTool("change_view", "Change view", {
                "type": "object", "additionalProperties": False,
            }, write, read_only=False, confirmation="Change view?"),
        ], **options)
        await session.flushed[0]()
        capabilities = next(payload for name, payload in session.messages if name == CAPABILITIES_MESSAGE)
        assert bool(capabilities["tools"]) is allow_writes
        # Supply the hidden tool name and a valid nonce directly to the Shiny input.
        browser_request.set({"version": 1, "session": capabilities["session"],
                             "requestId": "request_12345678", "name": "change_view", "arguments": {}})
        await reactive.flush()
        await asyncio.wait_for(session.responded.wait(), 1)
        response = next(payload for name, payload in session.messages if name == RESULT_MESSAGE)
        assert response["ok"] is allow_writes
        assert calls == ([{}] if allow_writes else [])
        if not allow_writes:
            assert response["code"] == "write_not_allowed"
        for callback in session.ended:
            callback()

    try:
        asyncio.run(run())
    finally:
        for registered in effects:
            registered.destroy()
