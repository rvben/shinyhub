## Restore fuzzing

From the repository root, run `make fuzz-py-bookmarks`. The bounded Hypothesis
suite generates 500 examples for each of eight properties by default. CI uses
deterministic generation; local exploration uses varied seeds. It checks initial root
GETs with arbitrary JSON bookmark values, choice values and labels, grouped
choices, single/multiple selection, and up to three module namespaces. It also
checks date/datetime scalar and range restoration with nested modules, malformed
date values, full-range dates and offset-aware datetimes, and choice-resolution validity and idempotence, JSON equality, input mutation,
and restore-context isolation. Valid selectize selections use Shiny's default
bookmark serializer, including null for an empty multi-select. The suite also
exercises raw queries with malformed JSON, repeated keys, unknown IDs, missing
markers, duplicate JSON keys and oversized numbers. These properties run in CI
on Shiny 1.8.0 and the newest available Shiny satisfying `>=1.8,<2`.

For a longer local run or a reproducible seed:

```console
make fuzz-py-bookmarks BOOKMARKS_FUZZ_EXAMPLES=10000 FUZZ_ARGS='--hypothesis-seed=8323'
```

Hypothesis shrinks failures and prints a reproduction blob. Its local
`.hypothesis/` database is ignored by Git. The generators bound nesting and
collection sizes; this suite does not establish safety for unbounded payloads.

`make audit-py-bookmarks AUDIT_ARGS='--examples 500 --seed 8323'` separately
searches 17 native/helper input configurations and prints JSON lines containing
minimized render failures. It distinguishes representative saved values from arbitrary
JSON values, including wrong shapes that could come from edited URLs or changed
app schemas. A successful search is an observation, not an assertion that an
upstream crash must continue to exist. No finding within the search budget does
not establish that a control is safe. This audit checks initial rendering only.

For a browser-generated date/datetime-slider bookmark round trip, first run
`make install-py-bookmarks-browser`, then `make audit-py-bookmarks-browser`.
This prints the actual query and server values before and after navigation,
and exits nonzero if the state changes or an unhandled page error occurs.
The default fixture uses both compatibility helpers and should preserve state.
Pass `AUDIT_ARGS='--native-sliders'` to explore upstream date-slider behavior
without the helper. On Shiny 1.8/1.9, native date sliders reopen at their minimum
despite HTTP 200. This exploratory upstream audit stays outside the passing CI
properties. Select a Shiny version with `BOOKMARKS_SHINY='shiny==1.9.0'`.
Audits use local fixture apps and do not contact deployed dashboards.

The selectize markup property checks the helper's compatibility contract:
preserve native selection semantics while retaining all available options.
Exact selections are checked for well-shaped saved values; wrong-shaped values
must retain choices and select only from those choices. Browser fallback and
session-level `ChoiceRestore` policy still apply. This property does not promise
to sanitize all saved values into valid live selections.

The audits use private Shiny request/serialization APIs, as the SDK's ordinary
root-GET tests do. A native render exception is recorded by type and message;
the real HTTP response handling may turn that exception into a 500. The search
reports one minimized failure per configuration, rather than an exhaustive
list of failure classes. Browser output includes changed field names and uses
UTC with stable server-value sampling. The fixture also bookmarks the helper's
single and multiple selections to check that those survive actual save/restore.
No upstream bugs are encoded as tests that require those bugs to remain
present: the native browser audit will exit successfully when upstream fixes
the round trip. The passing suite verifies the helpers, including malformed
date fallback and correct millisecond values for valid saved dates.

Every browser control is changed from its default before saving, and the audit
asserts that all fields changed. Run with `AUDIT_ARGS='--drop-query'` as a
negative control: discarding the bookmark must change all four restored fields
and exit nonzero. Firefox and WebKit are supported through `--browser` after
installing those Playwright browsers separately; the install target installs
Chromium only. HTML choice generation excludes control characters, while JSON
value comparison and restoration include tabs, newlines and carriage returns.
