from shiny import App, ui

print("shinyhub browser logs E2E", flush=True)

app_ui = ui.page_fluid(
    ui.h1("shinyhub remote-worker E2E"),
    ui.p("ok"),
)


def server(input, output, session):
    pass


app = App(app_ui, server)
