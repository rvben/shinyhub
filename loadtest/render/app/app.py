"""Synthetic Shiny app with a tunable, CPU-bound render cost.

Models a heavy plotly dashboard: six outputs whose combined first render
costs RENDER_COST_MS of real CPU, roughly independent of any data size.
Every output depends on the Apply button, so one click re-renders all six,
and the outputs are split across two tabs so a tab switch also forces
re-renders of the newly visible panel.

The six outputs are spelled out rather than generated in a loop. Shiny binds
an output by the decorated function's __name__, so a loop needs either the
deprecated @output(id=...) form or __name__ rewriting, and both are sensitive
to the Shiny version. A break there would look like a rig failure rather than
an API change, so the repetition is the safer trade here.
"""
import os
import time

from shiny import App, render, ui

RENDER_COST_MS = int(os.environ.get("RENDER_COST_MS", "1300"))
RENDER_OUTPUTS = 6
PER_OUTPUT_MS = RENDER_COST_MS / RENDER_OUTPUTS


def burn(ms):
    """Occupy the CPU for ms milliseconds.

    A busy loop, never time.sleep: the rig measures contention for a
    2-vCPU guest, and a sleeping render would contend for nothing.
    """
    deadline = time.perf_counter() + (ms / 1000.0)
    total = 0.0
    while time.perf_counter() < deadline:
        for _ in range(1000):
            total += 1.0000001 * 1.0000001
    return total


app_ui = ui.page_fluid(
    ui.input_action_button("apply", "Apply"),
    ui.navset_tab(
        ui.nav_panel(
            "Tab A",
            ui.output_text_verbatim("out_a_0"),
            ui.output_text_verbatim("out_a_1"),
            ui.output_text_verbatim("out_a_2"),
            value="tab_a",
        ),
        ui.nav_panel(
            "Tab B",
            ui.output_text_verbatim("out_b_0"),
            ui.output_text_verbatim("out_b_1"),
            ui.output_text_verbatim("out_b_2"),
            value="tab_b",
        ),
        id="tabs",
    ),
    ui.output_text_verbatim("rig_ready"),
)


def server(input, output, session):
    @render.text
    def out_a_0():
        input.apply()
        burn(PER_OUTPUT_MS)
        return "a0"

    @render.text
    def out_a_1():
        input.apply()
        burn(PER_OUTPUT_MS)
        return "a1"

    @render.text
    def out_a_2():
        input.apply()
        burn(PER_OUTPUT_MS)
        return "a2"

    @render.text
    def out_b_0():
        input.apply()
        burn(PER_OUTPUT_MS)
        return "b0"

    @render.text
    def out_b_1():
        input.apply()
        burn(PER_OUTPUT_MS)
        return "b1"

    @render.text
    def out_b_2():
        input.apply()
        burn(PER_OUTPUT_MS)
        return "b2"

    @render.text
    def rig_ready():
        return "RIG_READY"


app = App(app_ui, server)
