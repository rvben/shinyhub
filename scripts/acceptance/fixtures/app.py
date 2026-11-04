from shiny import App, render, ui


def server(input, output, session):
    print("Acceptance session connected", flush=True)

    @render.text
    def forecast():
        return f"Forecast: {input.demand()}"


app = App(
    ui.page_fluid(
        ui.h2("Acceptance forecast"),
        ui.input_numeric("demand", "Demand", 35, min=0, max=100),
        ui.output_text("forecast"),
    ),
    server,
)
