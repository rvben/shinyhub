from shiny import App, ui, render, module
from shiny.module import ResolvedId
from shinyhub_bookmarks import input_selectize


@module.ui
def late_control():
    return input_selectize(
        "late_res", "Dynamic resolution", ["Day", "Week", "Month"], selected="Month"
    )


def app_ui(request):
    return ui.page_fluid(
        input_selectize(
            "res", "Resolution", ["Day", "Week", "Month"], selected="Month"
        ),
        input_selectize(
            "multi",
            "Multiple",
            ["Day", "Week", "Month"],
            selected=["Month"],
            multiple=True,
        ),
        ui.output_ui("late"),
        ui.output_code("values"),
    )


def server(input, output, session):
    @render.ui
    def late():
        return late_control("m")

    @render.code
    def values():
        return repr((input.res(), input.multi(), input[ResolvedId("m-late_res")]()))


app = App(app_ui, server, bookmark_store="url")
