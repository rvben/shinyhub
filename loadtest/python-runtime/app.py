"""Fixed-work Shiny fixture for interpreter comparisons with real sessions."""

from importlib.metadata import version
import json
import os
import sys
import time

from shiny import App, render, ui


app_ui = ui.page_fluid(
    ui.input_action_button("calculate", "Calculate"),
    ui.output_text_verbatim("runtime"),
    ui.output_text_verbatim("result"),
)


def server(input, output, session):
    @render.text
    def runtime():
        jit = getattr(sys, "_jit", None)
        return json.dumps({
            "python": sys.version.split()[0], "pid": os.getpid(),
            "gil_enabled": getattr(sys, "_is_gil_enabled", lambda: True)(),
            "jit_enabled": bool(jit and jit.is_enabled()),
            "shiny": version("shiny"),
            "pydantic": version("pydantic"),
            "pydantic_core": version("pydantic-core"),
        })

    @render.text
    def result():
        # Fixed work, rather than a deadline loop: faster runtimes do less
        # elapsed work, so the benchmark can actually detect a speedup.
        calculation = input.calculate()
        started = time.perf_counter_ns()
        total = 0
        for i in range(100000):
            total = ((total ^ i) * 1664525 + 1013904223) & 0xFFFFFFFF
        compute_ms = (time.perf_counter_ns() - started) / 1_000_000
        return f"Calculation: {calculation}; value: {total}; compute_ms: {compute_ms:.6f}"


app = App(app_ui, server)
