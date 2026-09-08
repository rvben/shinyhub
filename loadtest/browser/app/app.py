"""Small real Shiny dashboard for browser lifecycle acceptance checks."""
from pathlib import Path
import secrets

from shiny import App, render, ui

VERSION = Path(__file__).with_name("version.txt").read_text().strip()

app_ui = ui.page_fluid(
    ui.h2("Lifecycle dashboard"),
    ui.p("Change the sample count and recalculate to verify the live session."),
    ui.input_numeric("samples", "Samples", value=1000, min=1, max=100000),
    ui.input_action_button("calculate", "Recalculate"),
    ui.output_text_verbatim("result"),
)


def server(input, output, session):
    session_label = secrets.token_hex(4)

    @render.text
    def result():
        count = max(1, min(100000, int(input.samples())))
        values = range(1, count + 1)
        return (
            f"Version: {VERSION}\nSession: {session_label}\n"
            f"Calculation: {input.calculate()}\nSamples: {count}\n"
            f"Sum: {sum(values)}\nMean: {sum(values) / count:.1f}"
        )


app = App(app_ui, server)
