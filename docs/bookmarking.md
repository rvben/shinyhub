---
description: "Add selective view links to Python Shiny apps with the shinyhub-bookmarks helper and ShinyHub's user-friendly app switcher."
---

# Link to a Shiny view

ShinyHub can offer **Link to this view** inside the app switcher when a Python
Shiny app opts in. The app names the filter inputs that are safe and useful to
carry. ShinyHub presents the receipt and selection UI; Shiny itself serializes
and restores the state.

There is no dedicated ShinyHub bookmark API or database table. Shiny serializes
the selected state into the URL, which follows the application's normal request
path when opened.

Registered filters also keep the current address synchronized. The live address
contains only values that differ from the app's baseline. Changes are debounced
and replace the current history entry, so refresh and ordinary browser bookmarks
reopen the same view without turning every slider step into a Back button stop.
**Link to this view** remains the exact, selective sharing control: a visitor can
exclude registered fields before copying a link.

## Add it to an app

Install `shinyhub-bookmarks` alongside Shiny 1.6.4 or newer:

```console
uv add shinyhub-bookmarks
# or: python -m pip install shinyhub-bookmarks
```

Then add the browser dependency to the UI and register fields in the server:

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
            "region": Field("Region", baseline="Europe"),
            "year": Field("Reporting year", baseline=2026),
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

An exact link that is opened stays exact until a registered value changes. At
that point, live synchronization rewrites it to the minimal representation.

**Change** reveals checkboxes in place; **Done** returns to the compact review.
Unchecked values are omitted and use whatever defaults the app has when the
link is opened. The copy action is disabled when no values are selected,
avoiding a link that looks special but carries no useful state. The panel also
reminds visitors that the app's access rules still apply to anyone opening the
link.

## Keep the live URL minimal

Use `baseline=` for a stable initial value that may be omitted from the live
URL:

```python
fields={
    "forecast": Field("Forecast", baseline=False),
    "segments": Field("Segments", baseline=[]),
}
```

`baseline=` is comparison metadata; it does not set the Shiny control's value.
Keep it aligned with the control's initialized value. On a clean page, a
mismatch is logged without logging either value, and the current value remains
in live URLs until the control reaches the declared baseline. This preserves
refresh fidelity while still allowing later initialization code to settle on
the declared value.

The live URL contains a field exactly when its current value differs
semantically from its baseline. `False`, `0`, an empty string, and an empty
selection are real values—not shorthand for “omit.” Returning a field to its
baseline removes it again.

When `baseline=` is omitted, the helper learns the first materialized value on
a clean page. This also works for dynamically rendered inputs: learning waits
until the input exists. If later initialization code changes that first value,
declare the final value explicitly with `baseline=`.

Restoration and minimization are intentionally separate. `ChoiceRestore.default`
is only the fallback for a saved choice that is no longer available; it is not
a URL baseline. If a field is restored from an existing URL before a baseline
is known, the helper retains that field in later live URLs rather than risk
losing part of the current view on refresh.

For example, if `region="Europe"` and `year=2026` are baselines, a clean view
uses the app path with neither input in its query. Changing only the region adds
only `region`. The explicit **Copy link** action still includes every checked
field, including `year=2026`, because it represents the exact selected view.

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
differs from its baseline it becomes part of the current URL, and generated
links may appear in browser history, logs, and referrer data.
The browser-local ShinyHub switcher receives registered display values and the
generated URL to render the receipt and copy the link. ShinyHub does not
intentionally retain a separate bookmark-state copy. As with any application
URL, the query travels through the browser, ShinyHub's normal proxy path, and
Shiny; depending on deployment configuration, it may also appear in access
logs, analytics, browser history, and referrer data.

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
Live synchronization may select no fields when every value is at baseline; in
that case the bridge removes Shiny's empty input marker while preserving the
fragment, if any. If the app adds its own `state.values` in a bookmark callback,
Shiny's `_values_` query remains because it is application-owned state.

## Evolving view links safely

View links often outlive the release that created them. Shiny ignores an
input that no longer exists, but an unavailable choice or renamed input can
otherwise fall back silently. Add restore rules to make that behavior explicit:

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

The restore callback uses Shiny's public bookmark lifecycle hooks. It validates
saved choice values, applies aliases, updates `select`, `selectize`, or `radio`
controls, and reports any adjustment to the browser-local switcher. A multiple
selection keeps its still-valid members in current display order, including an
empty selection. Removed fields listed in `legacy_fields` are ignored and
reported. `renamed_from` requires a `ChoiceRestore` policy so the saved value is
validated and moved to the current field. For a dynamically rendered choice
input, validation and any required update run once the input materializes.

The helper adds no package-specific schema or version metadata to the URL.
Shiny may still include application-owned bookmark values when an app adds
them through its own callbacks. App evolution is declared directly through
`renamed_from`, `legacy_fields`, and restore rules.

The recovery UX is deliberately non-blocking. The app opens the closest current
view, the link control gains an amber status dot, and its **Opened with changes**
receipt labels the saved and opened values for anything updated, unavailable,
renamed, removed, or ignored. Completely unknown
inputs show their URL-provided IDs and saved values as escaped, length-bounded
plain text. The first three are listed and any remainder is summarized. Copying
**Copy link to current view** creates a fresh link from the current app and
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
