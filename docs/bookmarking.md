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
selection are real values, not shorthand for “omit.” Returning a field to its
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
Query parameters the page was opened with (for example `?lang=nl` or campaign
tags) stay in the address in front of the bookmark state. Shiny ignores every
parameter before its first `_inputs_` or `_values_` marker, so they never reach
the restored inputs. In an app that opts into `session.allow_reconnect()`, a
reconnect to a new server session keeps the address in step: the bridge
recognizes the new session and drops the old session's unanswered save
instead of waiting behind it.

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

## Update hidden dashboards automatically

Filter-only dashboards can opt into switching to a newly deployed generation
while their tab is hidden.

Use ShinyHub 0.20.4 or later and `shinyhub-bookmarks` 0.5.4 or later. Adapter
0.5.3 has a discovery-ordering bug that closes sessions on page load. Adapters
before 0.5.3 do not advertise the pending-sync protocol, so switching stays manual.

In the app's `shinyhub.toml`:

```toml
[app]
version_switch = "when_hidden" # default: "manual"
```

This is the app author's declaration that its registered bookmark filters are
sufficient to restore the view. Audit every meaningful filter and test its URL
round-trip before enabling it. Forms and editors should retain `manual` unless
all work they promise to preserve is covered by that contract. ShinyHub cannot
infer whether arbitrary application state is unsaved.

Automatic switching currently supports multiplex pools only. Grouped and
per-session pools, including retained elastic workers during an isolation
change, keep manual switching because their worker identity is shared between
tabs. The server checks this again when an automatic switch is requested.

The switcher keeps checking for a ready generation while hidden, with a slower
poll interval. It switches after at least 30 seconds continuously hidden, only
when restoration has completed and the latest registered filter state is saved
in the URL. Open chat or bookmark panels, busy chats, pending link creation, and session
snapshots prevent the switch. Returning to the tab or changing a filter while
the switch request is in flight cancels automatic navigation. A failed attempt
leaves the manual notice; it does not repeatedly reload the same generation.

The reload uses the current URL, including its query and hash. The switcher
shows a dismissible “Updated to the latest version” notice afterwards. Newer
data may be displayed; unregistered state such as plot zoom, table sorting,
scroll position or a selected navigation tab is not preserved. Visible tabs
retain the manual notice and the configured generation retirement deadline.

Automatic switching requires an adapter with the pending-sync protocol below.
A restored dynamic input that never appears keeps restoration unsettled and
blocks automatic switching.

Older adapters continue to support bookmarks and manual switching, but cannot
opt into unattended reloading safely. Deploying the hub alone does not upgrade
an adapter installed in an app's environment. The policy belongs to the bundle
that served the page, so a later deploy cannot retroactively opt an older page in.

### Recovering an interrupted dashboard

The same `version_switch = "when_hidden"` opt-in also allows automatic recovery
after a session disconnects, including in a visible tab. The app switcher and
status overlay must both be enabled. Recovery reloads the current URL to restore
the registered filters; it does not resume the old server session or preserve
unregistered state. A brief page refresh may be visible.

Recovery requires the adapter's explicit disconnect signal described below and
a fully saved bookmark immediately before the disconnect. Pending edits,
unsettled restoration, open bookmark or chat panels, busy chats, and snapshots
keep manual recovery. A ready response is verified before reloading; a verified
cold-app response also permits reloading to wake the app. Network failures and
sign-in redirects do not trigger a reload.

Fast recovery skips the interruption dialog and Shiny's grey disconnect veil.
The pointer blocker remains in place, and keyboard edits still cancel automatic
restoration. Shiny has a 750 ms grace period to reconnect the existing session
before a reload is attempted. After 1.2 seconds, a small status notice explains
that the dashboard is reconnecting without taking keyboard focus. Its **Keep
these results** action cancels recovery and opens the retained page as an offline
snapshot. The manual fallback offers the same action.

When the browser is offline, the notice explains this and checks again as soon
as the connection returns. Sign-in and access failures stop automatic recovery
and offer a new tab, preserving the current results. Readiness requests time out
after 10 seconds; responses from cancelled or superseded requests are ignored.
Automatic recovery is limited to one attempt per minute per tab and app;
if browser session storage is unavailable, recovery stays manual. A stalled
recovery falls back to manual actions within 60 seconds. Updating ShinyHub alone
does not enable this behavior for an older bookmark adapter.

### Pending-sync adapter contract

Protocol version 1 capabilities add `pendingSync: true`, `syncReady` (restoration
has settled), `session` (an identity for this Shiny session), `syncRevision`,
`registeredInputs` (all registered input IDs), and `observedBrowserRevision`
(the browser change counter observed by the server). `syncFields` remains the
subset that needs encoding in the bookmark URL.

The browser bridge emits `shinyhub:bookmark:sync-status` with `pending` immediately
on a registered DOM input or `shiny:inputchanged` event. DOM input catches edits
that Shiny has not yet submitted, including inputs that update on blur. The
bridge sends its change counter in the same Shiny batch as the input; the
server echoes that counter, so unchanged values and type conversions cannot
leave a clean filter pending forever. It keeps that state until the server has
observed the latest browser changes and a matching serialization has updated the URL. An older response cannot
clear a newer edit. Sync statuses include `pendingSync`, `session`,
`syncRevision`, and `browserRevision`; only a matching `saved` status permits
an automatic switch. Missing capability, unsettled restore, disconnect, or
serialization failure blocks it. The Python adapter implements this contract;
other adapters must implement the same safeguards before advertising it.

On disconnect, the adapter emits a `pending` status with `code: "disconnected"`,
retaining the session, sync revision, and browser revision. This still blocks
automatic version switching. For session recovery only, the switcher can retain
the immediately preceding `saved` checkpoint if all three values match and the
page URL has not changed. Every other pending or error status invalidates that
checkpoint. Adapters that emit an unqualified `pending` on disconnect retain
manual recovery.
