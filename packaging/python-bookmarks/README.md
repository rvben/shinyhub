# shinyhub-bookmarks

Selective view links for Python Shiny apps running in ShinyHub, built on
Shiny's URL-bookmarking API.

The package registers the filters an app author considers meaningful. ShinyHub
then adds a **Link to this view** control to its app switcher. Visitors can copy
the exact view immediately or use **Change** to choose which registered values
follow the link. Unselected values return to the app's defaults when the link
opens. The visible UI says “link”; package and API names retain “bookmark” to
match Shiny's native lifecycle.

```python
from shiny import App, render, ui
from shinyhub_bookmarks import ChoiceRestore, Field, bookmarking_dependency, register


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
so Shiny can restore URL state before rendering it. Call `register()` exactly
once from the top-level server session. Module inputs can be included by their
resolved IDs in that top-level field mapping; module-scoped registration is
rejected because selective exclusion is owned by the root bookmark session.

After a registered filter changes, the bridge also updates the current address
after a short debounce. It replaces the current browser-history entry, so a
refresh or ordinary browser bookmark reopens the same view without filling the
Back button with every intermediate slider or text-input value. The explicit
**Link to this view** action remains the place to exclude selected fields before
sharing.

## View links that outlive the app

Native Shiny restoration ignores a removed input, but a retired choice can
otherwise become empty or fall back differently across widgets. Declare current
choices when a bookmark should remain dependable across app releases:

```python
register(
    session=session,
    input=input,
    legacy_fields={"segment": "Market segment"},
    fields={
        "region": Field(
            "Region",
            restore=ChoiceRestore(choices=REGIONS, default="Europe"),
            renamed_from={"territory": "Territory"},
        ),
        "product": Field(
            "Product",
            restore=ChoiceRestore(
                choices=lambda: PRODUCTS,
                default="All products",
                aliases={"Legacy planning": "Planning"},
                control="select",
            ),
        ),
    },
)
```

`ChoiceRestore` validates the saved value, applies aliases, and updates the
current Shiny choice input. Supported controls are `select`, `selectize`, and
`radio`. Multiple selections retain every choice that still exists, use the
current display order, and preserve an empty selection. A missing declared
default falls back to the valid value Shiny already selected.

`renamed_from` maps an old input ID to its former label and requires a
`ChoiceRestore` policy so the saved value can actually be applied to the new
control. `legacy_fields` names removed inputs that should be reported as
ignored. The helper adds no package-specific schema or version metadata to the
URL. Shiny may still include application-owned bookmark values when an app
adds them through its own callbacks.

For a custom input whose live Python value differs from its JSON bookmark
representation, pass an idempotent `normalizer=` to `Field`. It is applied to
both sides before comparison; it does not change the value Shiny serializes or
restores.

When anything changes, the switcher marks the link action and presents a
plain-language **Opened with changes** receipt. It labels saved and opened
values for migrated, unavailable, renamed, and removed fields, then offers
**Copy link to current view**. The app still opens; stale state is never
promoted into a blocking error.

An input that is neither registered nor declared as renamed or removed is shown
with its URL-provided ID and saved value, labelled **Not recognized**. Both are rendered
as bounded plain text. At most three unknown inputs are listed before a compact
overflow summary. Copying the updated link drops every unknown setting, so the
warning clears on the next visit.

## Privacy and behaviour

- The browser-local ShinyHub switcher receives the registered display values and
  generated URL so it can show the receipt and copy the link. After a registered
  value changes, all registered values also become part of the current browser
  URL so refresh works. The ShinyHub server neither receives nor persists
  bookmark state; selected values stay in Shiny's URL.
- Every registered field is selected by default, including values equal to the
  app's current defaults. The receipt makes that scope explicit before copying.
- App inputs not registered here are always excluded.
- A view link with no selected fields cannot be created.
- URLs over 8 KiB are rejected by default. Raise `max_url_length` only when the
  complete delivery path is known to accept longer URLs.
- The control stays absent when the bridge is not installed, so unsupported apps
  never show a dead action.
