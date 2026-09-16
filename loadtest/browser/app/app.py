"""Small real Shiny dashboard for browser lifecycle acceptance checks."""
from pathlib import Path
import secrets

from shiny import App, reactive, render, ui
from shinyhub_identity.shiny import session_identity

VERSION = Path(__file__).with_name("version.txt").read_text().strip()

app_ui = ui.page_fluid(
    ui.h2("Lifecycle dashboard"),
    ui.p("Change the sample count and recalculate to verify the live session."),
    ui.input_numeric("samples", "Samples", value=1000, min=1, max=100000),
    ui.input_action_button("calculate", "Recalculate"),
    ui.output_text_verbatim("result"),
    ui.input_action_button("export_report", "Export report"),
    ui.output_text_verbatim("export_result"),
)


def server(input, output, session):
    session_label = secrets.token_hex(4)
    user = session_identity(session)
    exports = reactive.value(0)
    export_status = reactive.value("Not requested")

    # Leave the button enabled: the acceptance check must exercise the server's
    # authorization decision, not merely a hidden or disabled browser control.
    @reactive.effect
    @reactive.event(input.export_report)
    def _():
        if user is None or "report_export" not in user.entitlements:
            export_status.set("Denied")
            return
        exports.set(exports.get() + 1)
        export_status.set("Allowed")

    @render.text
    def export_result():
        return f"Result: {export_status.get()}\nExports: {exports.get()}"

    @render.text
    def result():
        count = max(1, min(100000, int(input.samples())))
        values = range(1, count + 1)
        return (
            f"Version: {VERSION}\nSession: {session_label}\n"
            f"User: {user.username if user else 'anonymous'}\n"
            f"Role: {user.role if user else 'anonymous'}\n"
            f"Entitlements: {','.join(user.entitlements) if user else ''}\n"
            f"Calculation: {input.calculate()}\nSamples: {count}\n"
            f"Sum: {sum(values)}\nMean: {sum(values) / count:.1f}"
        )


app = App(app_ui, server)
