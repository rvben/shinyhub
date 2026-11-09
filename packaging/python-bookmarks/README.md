# shinyhub-bookmarks

Selective view links for Python Shiny apps running in ShinyHub, built on
Shiny's URL-bookmarking API.

The package registers the filters an app author considers meaningful. ShinyHub
then adds a **Link to this view** control to its app switcher. Visitors can copy
the exact view immediately or use **Change** to choose which registered values
follow the link. Unselected values return to the app's defaults when the link
opens. The visible UI says “link”; package and API names retain “bookmark” to
match Shiny's native lifecycle.

```console
uv add shinyhub-bookmarks
# or: python -m pip install shinyhub-bookmarks
```

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
            "region": Field("Region", baseline="Europe"),
            "year": Field("Year", baseline=2026),
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
after a short debounce. The live address contains only values that differ from
their baselines. It replaces the current browser-history entry, so a refresh or
ordinary browser bookmark reopens the same view without filling the Back button
with every intermediate slider or text-input value. The explicit **Link to this
view** action remains the exact, selective sharing control.

For an app with an Apply button, capture the displayed filters only when the
user commits them:

```python
register(
    session=session,
    input=input,
    fields={"region": "Region"},
    commit=input.apply,
)
```

All registered fields use this trigger. The helper captures the initial inputs
and snapshots them again on Apply. Restore corrections settle into the initial
committed view before draft edits are isolated. A later-rendered field gets its initial
committed value when it first appears. Draft edits leave the URL and saved status
unchanged; both automatic switching and copied links use the captured values.
Use the same Apply input that commits the app's displayed view. Its browser
event marks synchronization pending until the committed URL has been saved.

Declare stable baselines with `Field(baseline=...)`. This is comparison
metadata; it does not set the Shiny control's initial value. On a clean page,
the helper logs a mismatch without logging either value and safely retains the
current value until it reaches the declaration. Without a declaration, it
learns the first materialized value—even for a dynamically rendered input.
Declare `baseline=` when later initialization code changes that first value.

Baseline comparison is semantic. Meaningful falsy values such as `False`, `0`,
and an empty selection remain in the URL when they differ from the baseline.
`ChoiceRestore.default` has a separate job: it is only the fallback for an
unavailable saved choice. A field restored before its baseline is known remains
in later live URLs for refresh safety.

The live URL is minimal; the explicit **Copy link** action is exact and includes
every checked field, even one at baseline. An opened exact link stays full until
a registered value changes, after which live synchronization minimizes it.

## Selectize inputs

Build bookmarkable selectize inputs with this package's helper:

```python
from shinyhub_bookmarks import input_selectize

input_selectize(
    "resolution", "Resolution", ["Day", "Week", "Month"], selected="Month"
)
```

Replace `ui.input_selectize(...)` with `input_selectize(...)` for both single
and multiple selections. The arguments and defaults are the same. Modules,
grouped choices, and dynamically rendered inputs are supported.

Shiny 1.8 and 1.9 restore the bookmarked selection into the available choices.
This causes single selections and `null` selections to fail before the page
loads, and narrows multi-select choices to the bookmarked values. The helper
restores only the selection and preserves the app's choices. It also works
with the upstream fix in [py-shiny #2610](https://github.com/posit-dev/py-shiny/pull/2610).
It does not patch Shiny or change other inputs.

Upgrading this package alone does not fix existing `ui.input_selectize` calls;
apps must switch to the helper. This applies to any selectize included in a
native Shiny bookmark, whether or not it is registered with ShinyHub.
`register()` and `ChoiceRestore(control="selectize")` run after UI creation
and cannot prevent an initial rendering failure. Continue to use `ChoiceRestore`
for retired choices, aliases, and adjustment messages.

The helper can remain in place when Shiny includes the upstream fix. No private
context API is needed for inputs without a saved value; bookmarked inputs use
a short-lived context scoped to the constructor call.

For maintainers, `make test-py-bookmarks` runs the package tests.
Run `make install-py-bookmarks-browser` to install Chromium.
`make test-py-bookmarks-browser` checks initial and dynamically
rendered module inputs in a real browser, including selecting options omitted
from the bookmark. Both targets resolve the same dependency environment, and
the browser tests use an editable installation so source changes are exercised.
Set `BOOKMARKS_BROWSERS='--browser chromium --browser firefox --browser webkit'`
to exercise all three browser engines.

## Date and datetime sliders

Build bookmarkable date and datetime sliders with this package's helper:

```python
from datetime import date
from shinyhub_bookmarks import input_slider

input_slider(
    "period", "Period", date(2026, 1, 1), date(2026, 12, 31),
    (date(2026, 2, 1), date(2026, 3, 1)),
)
```

Replace `ui.input_slider(...)` with `input_slider(...)` for sliders whose bounds
are Python `date` or `datetime` values. Both single values and ranges are
supported, including modules and dynamically rendered inputs. Numeric sliders
can keep using `ui.input_slider`. The helper has the same arguments and defaults.

Shiny 1.8 and 1.9 can silently reset bookmarked date sliders to their minimum
because saved dates reach the browser as strings instead of numeric timestamps.
The helper decodes the saved value before delegating rendering and timestamp
conversion to Shiny. It also works with the upstream fix in
[py-shiny #2612](https://github.com/posit-dev/py-shiny/pull/2612).

Upgrading this package alone does not change existing `ui.input_slider` calls.
Switch each affected constructor, then reopen a bookmark with a non-default
value and verify both the control and its server value. This applies to native
Shiny bookmarks as well as fields registered with ShinyHub. `register()` runs
after UI construction and cannot repair this initial rendering problem.

Invalid saved dates, null values, and mismatched single/range shapes use the
app's default value. Registered fields can report that adjustment through the
existing banner. Valid dates outside the current bounds retain Shiny's normal
clamping behavior. Numeric inputs retain native Shiny restoration behavior.
No global monkeypatch is installed; the helper can remain after an upstream
upgrade. Only saved date inputs need the guarded private restore-context API.

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
current display order, and preserve an empty selection. Whether a field takes
one value or several follows the live control, not the saved link: a saved list
restores into a single choice only when it holds exactly one item (otherwise
the field falls back), and a saved single value restores into a multiple
selection as a one-item selection. A missing declared
default falls back to the valid value Shiny already selected. Dynamically
rendered choice inputs are validated once they materialize.

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
  generated URL so it can show the receipt and copy the link. ShinyHub has no
  dedicated bookmark-state API or store. The URL query still follows the
  browser, proxy, and Shiny request path and may appear in browser history,
  deployment logs, analytics, or referrer data.
- Every registered field is selected by default, including values equal to the
  app's current defaults. The receipt makes that scope explicit before copying.
- Helper-created links exclude unregistered Shiny inputs. App-owned bookmark
  callbacks may still add `state.values`, which Shiny preserves as `_values_`.
- A view link with no selected fields cannot be created.
- URLs over 8 KiB are rejected by default. Raise `max_url_length` only when the
  complete delivery path is known to accept longer URLs.
- The control stays absent when the bridge is not installed, so unsupported apps
  never show a dead action.

Restore property tests and exploratory browser audits are documented in
[fuzz-tests/README.md](fuzz-tests/README.md).
