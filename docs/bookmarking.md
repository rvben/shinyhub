---
description: "Add selective view links to Python Shiny apps with the shinyhub-bookmarks helper and ShinyHub's user-friendly app switcher."
---

# Link to a Shiny view

ShinyHub can offer **Link to this view** inside the app switcher when a Python
Shiny app opts in. The app names the filter inputs that are safe and useful to
carry. ShinyHub presents the receipt and selection UI; Shiny itself serializes
and restores the state.

There is no ShinyHub bookmark table, API, or retained copy of filter values.
The state stays in the URL returned by Shiny.

Registered filters also keep the current address synchronized. Changes are
debounced and replace the current history entry, so refresh and ordinary browser
bookmarks reopen the same view without turning every slider step into a Back
button stop. **Link to this view** remains the selective sharing control: a
visitor can exclude registered fields before copying a link.

## Add it to an app

Install `shinyhub-bookmarks` alongside Shiny 1.6.4 or newer, then add the browser
dependency to the UI and register fields in the server:

```python
from shiny import App, ui
from shinyhub_bookmarks import ChoiceRestore, Field, bookmarking_dependency, register


def app_ui(request):
    return ui.page_fluid(
        bookmarking_dependency(),
        ui.input_select("region", "Region", ["Europe", "Americas", "Asia"]),
        ui.input_slider("year", "Year", 2020, 2026, 2026),
    )


def server(input, output, session):
    register(
        session=session,
        input=input,
        fields={
            "region": Field("Region"),
            "year": Field("Reporting year"),
        },
    )


app = App(app_ui, server, bookmark_store="url")
```

All three integration details matter:

1. The UI is a function accepting `request`, which lets Shiny restore URL state
   before it creates the controls.
2. `bookmarking_dependency()` loads the inert app-side browser bridge.
3. `bookmark_store="url"` enables Shiny's native URL serializer.

Call `register()` exactly once from the top-level server session. To bookmark
inputs rendered by modules, include their resolved IDs (for example,
`"filters-region"`) in this top-level field mapping. Calling `register()` from
inside a module is rejected: Shiny owns selective exclusion at the root
bookmark session, and accepting a module proxy could otherwise include inputs
outside the declared allow-list.

If the package is not present, the switcher simply has no view-link action.

## What visitors get

The panel lists every registered field and its current value. All values are
included by default, so **Copy link** means “copy this exact view,” including
values that happen to equal today's app defaults.

**Change** reveals checkboxes in place; **Done** returns to the compact review.
Unchecked values are omitted and use whatever defaults the app has when the
link is opened. The copy action is disabled when no values are selected,
avoiding a link that looks special but carries no useful state. The panel also
reminds visitors that the app's access rules still apply to anyone opening the
link.

## Field labels and values

A string is shorthand for a field label:

```python
fields={"region": "Region"}
```

Use a formatter when a raw value needs visitor-facing language:

```python
fields={
    "forecast": Field(
        "Forecast",
        formatter=lambda enabled: "Included" if enabled else "Actuals only",
    )
}
```

Formatters only affect the value shown in the panel. Shiny serializes the
original input value.
Do not register secrets or large free-form inputs: after a registered value
changes it becomes part of the current URL, and generated links may appear in
browser history, logs, and referrer data.
The browser-local ShinyHub switcher receives registered display values and the
generated URL to render the receipt and copy the link. The ShinyHub server does
not receive or persist bookmark state.

Transport differences involving dates, datetimes, tuples, mappings, enums,
UUIDs, and dataclasses are compared recursively. Custom inputs can provide an
idempotent comparison normalizer without changing serialization:

```python
fields={
    "custom": Field(
        "Custom filter",
        normalizer=lambda value: value.key if hasattr(value, "key") else value,
    )
}
```

## Limits and errors

The helper rejects URLs longer than 8 KiB by default because long request
targets are not consistently accepted across browsers and reverse proxies. An
app can pass `max_url_length=` to `register()` when its entire delivery path has
a known higher limit.

The adapter serializes one request at a time, applies its allow-list to the
individual bookmark state without mutating the app's shared
`bookmark.exclude` list, validates selected IDs against the registration, and
returns stable browser error codes. It does not log filter values or generated
URLs.

Automatic saving is deliberately bounded. A transient failure is retried once;
if the URL still cannot be updated, the link control gains a coral status dot
and explains that **Copy link** will preserve the latest filters. A later filter
change retries normally and clears the warning after the URL is safely updated.

## Evolving view links safely

View links often outlive the release that created them. Shiny ignores an
input that no longer exists, but an unavailable choice or renamed input can
otherwise fall back silently. Add restore rules to make that behavior explicit:

```python
register(
    session=session,
    input=input,
    schema_version=3,
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

The restore callback uses Shiny's public bookmark lifecycle hooks. It validates
saved choice values, applies aliases, updates `select`, `selectize`, or `radio`
controls, and reports any adjustment to the browser-local switcher. A multiple
selection keeps its still-valid members in current display order, including an
empty selection. Removed fields listed in `legacy_fields` are ignored and
reported. `renamed_from` requires a `ChoiceRestore` policy so the saved value is
validated and moved to the current field.

New links include `schema_version` as Shiny bookmark metadata. The metadata is
for migrations and diagnostics; it is not server-side state. Links created by
the first helper release have no metadata and continue to restore.

The recovery UX is deliberately non-blocking. The app opens the closest current
view, the link control gains an amber status dot, and its **Opened with changes**
receipt labels the saved and opened values for anything updated, unavailable,
renamed, removed, or ignored. Completely unknown
inputs show their URL-provided IDs and saved values as escaped, length-bounded
plain text. The first three are listed and any remainder is summarized. Copying
**Copy link to current view** creates a fresh link from the current schema and
drops those unknown settings.

## Compatibility

The browser boundary is a versioned set of `CustomEvent`s, so a future R helper
can implement the same contract without changing the switcher. Version 1 uses:

- `shinyhub:bookmark:discover`
- `shinyhub:bookmark:capabilities`
- `shinyhub:bookmark:create`
- `shinyhub:bookmark:result`
- `shinyhub:bookmark:error`
- `shinyhub:bookmark:sync-status`

Applications should use the helper rather than emitting these events directly;
the protocol is documented to make the ownership boundary and upgrade path
clear.

See [`examples/bookmarking-demo`](../examples/bookmarking-demo/) for a runnable
four-filter app.
