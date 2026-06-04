from shiny import App, render, ui

app_ui = ui.page_fluid(
    ui.h2("Hello from ShinyHub"),
    ui.input_slider("n", "Pick a number", min=0, max=100, value=40),
    ui.output_text_verbatim("result"),
)


def server(input, output, session):
    @render.text
    def result():
        return f"{input.n()} doubled is {input.n() * 2}"


app = App(app_ui, server)
