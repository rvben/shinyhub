# shinyhub-bookmarks

Selective URL bookmarks for Python Shiny apps running in ShinyHub.

The package registers the filters an app author considers meaningful. ShinyHub
then adds a **Bookmark this view** control to its app switcher. Visitors see a
plain-language receipt, can copy the exact view immediately, or customise which
registered filters follow the link. Unselected filters return to the app's
defaults when the bookmark opens.

```python
from shiny import App, render, ui
from shinyhub_bookmarks import Field, bookmarking_dependency, register


def app_ui(request):
    return ui.page_fluid(
        bookmarking_dependency(),
        ui.input_select("region", "Region", ["Europe", "Americas", "Asia"]),
        ui.input_slider("year", "Year", 2020, 2026, 2026),
        ui.output_text_verbatim("summary"),
    )


def server(input, output, session):
    register(
        session=session,
        input=input,
        fields={
            "region": Field("Region"),
            "year": Field("Year"),
        },
    )

    @render.text
    def summary():
        return f"{input.region()} · {input.year()}"


app = App(app_ui, server, bookmark_store="url")
```

Both pieces are required: `bookmarking_dependency()` installs the tiny browser
bridge, and `register()` publishes the allow-list and creates links through
Shiny's native bookmarking API. The UI must be a function accepting `request`
so Shiny can restore URL state before rendering it.

## Privacy and behaviour

- The browser-local ShinyHub switcher receives the registered display values and
  generated URL so it can show the receipt and copy the link. The ShinyHub
  server neither receives nor persists bookmark state; selected values stay in
  Shiny's URL.
- Every registered field is selected by default, including values equal to the
  app's current defaults. The receipt makes that scope explicit before copying.
- App inputs not registered here are always excluded.
- A custom bookmark with no selected fields cannot be created.
- URLs over 8 KiB are rejected by default. Raise `max_url_length` only when the
  complete delivery path is known to accept longer URLs.
- The control stays absent when the bridge is not installed, so unsupported apps
  never show a dead action.
