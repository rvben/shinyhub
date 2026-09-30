"""Run inside the uv instrumentation overlay, before importing app code."""
import contextlib
import inspect
from functools import wraps
import os
import runpy
import sys
import types


def configure_asgi():
    if os.environ.get("SHINYHUB_TRACING_ASGI_EVENTS", "false").lower() == "true":
        return
    try:
        from opentelemetry.instrumentation.asgi import OpenTelemetryMiddleware
        original = OpenTelemetryMiddleware.__init__
        signature = inspect.signature(original)
        if "exclude_spans" not in signature.parameters:
            print("shinyhub: installed ASGI instrumentation cannot suppress send/receive spans", file=sys.stderr)
            return

        @wraps(original)
        def init(self, *args, **kwargs):
            # Frameworks may forward None for their default. Explicit lists,
            # including [], remain authoritative; positional arguments work too.
            bound = signature.bind_partial(self, *args, **kwargs)
            if bound.arguments.get("exclude_spans") is None:
                bound.arguments["exclude_spans"] = ["receive", "send"]
            original(*bound.args, **bound.kwargs)

        OpenTelemetryMiddleware.__init__ = init
    except ImportError:
        pass  # A non-ASGI job may not have Starlette installed.
    except Exception:
        print("shinyhub: ASGI span suppression unavailable", file=sys.stderr)


def job_span():
    try:
        from opentelemetry import trace
        from opentelemetry.propagate import extract
        carrier = {"traceparent": os.environ.get("TRACEPARENT", ""),
                   "tracestate": os.environ.get("TRACESTATE", "")}
        return trace.get_tracer("shinyhub.python").start_as_current_span(
            "process.run", context=extract(carrier),
            record_exception=False, set_status_on_exception=False)
    except Exception:
        print("shinyhub: job tracing unavailable; executing command once", file=sys.stderr)
        return contextlib.nullcontext()


def execute(mode, target, args):
    sys.argv = [target] + args
    if mode.endswith("module"):
        runpy.run_module(target, run_name="__main__", alter_sys=True)
    elif mode.endswith("script"):
        sys.path[0] = os.path.dirname(os.path.abspath(target))
        runpy.run_path(target, run_name="__main__")
    elif mode.endswith("code"):
        sys.argv[0] = "-c"
        module = types.ModuleType("__main__")
        module.__package__ = None
        module.__spec__ = None
        sys.modules["__main__"] = module
        exec(compile(target, "<string>", "exec"), module.__dict__)
    else:
        raise RuntimeError("unsupported ShinyHub Python launch mode")


def main():
    mode, target, *args = sys.argv[1:]
    configure_asgi()
    is_job = mode.startswith("job-")
    with job_span() if is_job else contextlib.nullcontext() as span:
        try:
            execute(mode, target, args)
        except BaseException as error:
            failed = not isinstance(error, SystemExit) or error.code not in (None, 0)
            if failed and span is not None:
                from opentelemetry.trace import StatusCode
                # Exception messages and stack traces may contain secrets.
                span.set_attribute("error.type", type(error).__name__)
                span.set_status(StatusCode.ERROR, "Python command failed")
            raise
    # The SDK shuts down at interpreter exit after the process span ends.
    # Never rerun application code after an instrumentation failure.


main()
