import asyncio

from shiny import Inputs, reactive, ui
from shiny._namespaces import Root
from shinyhub_agent import AgentTool, register
from shinyhub_agent._shiny import (
    CHAT_CAPABILITIES_MESSAGE,
    CHAT_EVENT_MESSAGE,
    CHAT_INPUT,
)

SCHEMA = {"type": "object", "properties": {}, "additionalProperties": False}


class Session:
    ns = Root

    def __init__(self):
        self.flushed = []
        self.ended = []
        self.messages = []
        self.updates = []

    def on_flushed(self, fn, once):
        self.flushed.append(fn)

    def on_ended(self, fn):
        self.ended.append(fn)

    def is_stub_session(self):
        return False

    def send_input_message(self, id, message):
        self.updates.append((id, message))

    async def send_custom_message(self, name, message):
        self.messages.append((name, message))


def test_registered_callbacks_read_update_flush_and_undo_without_app_plumbing(
    monkeypatch,
):
    # Shiny's graph lock is process-global; each asyncio.run below uses a fresh loop.
    from shiny.reactive._core import _reactive_environment

    monkeypatch.setattr(_reactive_environment, "_lock", None)

    async def run():
        session = Session()
        inputs = Inputs({"period": reactive.Value("week")})
        state = reactive.Value("week")
        observed = []

        @reactive.effect(session=None)
        def output():
            observed.append(state.get())

        def validate(args):
            assert reactive.lock().locked()
            assert inputs.period() == "week"

        async def write(args):
            assert reactive.lock().locked()
            previous = state.get()
            ui.update_select("period", selected="year")
            state.set("year")
            return {"period": state.get(), "previous": previous}

        async def undo(args, result):
            assert reactive.lock().locked()
            ui.update_select("period", selected=result["previous"])
            state.set(result["previous"])
            return {"period": state.get()}

        tools = register(
            session=session,
            input=inputs,
            tools=[
                AgentTool(
                    "write_view",
                    "Change view",
                    SCHEMA,
                    write,
                    read_only=False,
                    confirmation="Change view?",
                    validate=validate,
                    undo=undo,
                )
            ],
        )
        result = await tools.execute("write_view", {})
        assert result == {"period": "year", "previous": "week"}
        assert observed[-1] == "year"
        await tools.undo("write_view", {}, result)
        assert observed[-1] == "week"
        assert [update[1]["value"] for update in session.updates] == [
            ["year"],
            ["week"],
        ]
        assert not reactive.lock().locked()
        output.destroy()
        for fn in session.ended:
            fn()

    asyncio.run(run())


def test_session_end_cancels_model_work_without_holding_reactive_lock(monkeypatch):
    from shiny.reactive._core import _reactive_environment

    monkeypatch.setattr(_reactive_environment, "_lock", None)

    async def run():
        session = Session()
        chat_input = reactive.Value(None)
        inputs = Inputs({CHAT_INPUT: chat_input})
        started, cancelled = asyncio.Event(), asyncio.Event()

        async def read(args):
            return {}

        class Agent:
            async def run(self, *args, **kwargs):
                assert not reactive.lock().locked()
                started.set()
                try:
                    await asyncio.Event().wait()
                finally:
                    cancelled.set()
                yield {"type": "delta", "text": "unreachable"}

        register(
            session=session,
            input=inputs,
            tools=[AgentTool("read_view", "Read view", SCHEMA, read)],
            chat=Agent(),
        )
        await session.flushed[0]()
        nonce = next(
            message["session"]
            for name, message in session.messages
            if name == CHAT_CAPABILITIES_MESSAGE
        )
        chat_input.set(
            {
                "version": 1,
                "session": nonce,
                "requestId": "request_12345678",
                "message": "Question",
            }
        )
        await reactive.flush()
        await asyncio.wait_for(started.wait(), 1)
        assert not reactive.lock().locked()
        for fn in session.ended:
            fn()
        await asyncio.wait_for(cancelled.wait(), 1)
        await asyncio.sleep(0)
        assert any(
            name == CHAT_EVENT_MESSAGE and message["type"] == "error"
            for name, message in session.messages
        )

    asyncio.run(run())
