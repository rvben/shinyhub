---
description: "Develop one or every local-source app in a ShinyHub fleet with automatic manifests, shared inputs, isolated workspaces, and safe reloads."
---

# Fleet development

`shinyhub dev` discovers fleet context automatically. The command stays the
same; the directory you run it from chooses the scope.

## Develop one fleet app

From a local-source app declared by the nearest `fleet.toml`:

```bash
cd apps/sales-dashboard
shinyhub dev .
```

ShinyHub uses the manifest slug and composes the app's `[[bundle_file]]` inputs.
Editing either the app source or a shared input triggers a staged,
health-checked reload of only that app.

## Develop several apps

From the fleet root, the ordinary command starts every watchable local-source
app on an automatic port and prefixes concurrent output with its slug:

```bash
shinyhub dev .
shinyhub dev . --app sales-dashboard
shinyhub dev . --app sales-dashboard --app operations
shinyhub dev fleet.toml
```

`--app` is repeatable. The manifest slug is authoritative, so it cannot be
combined with `--slug`.

Git-backed entries are named and skipped by the fleet-root default because a
filesystem watcher needs a local checkout. Add `--all` when skipping anything
would be a mistake; ShinyHub then fails before starting unless every declared
app has a watchable local source.

## Isolation and changes

Every selected app receives an isolated generated workspace, data directory,
and default port. For a multi-app run, explicit `--state-dir` and `--data-dir`
values act as roots with one child directory per slug. An explicit `--port`
requires a single selected app.

Changes to `fleet.toml` require restarting `dev`. This keeps changes to
consumers and destinations from altering a running session unexpectedly.

`dev` consumes the fleet's app identity, source topology, shared bundle inputs,
and visibility when explicitly creating one remote app. It does not reconcile
projects, ownership, `[app.config]`, or pruning. Use `shinyhub fleet plan` and
`shinyhub fleet apply` for declarative configuration changes.

Use `--standalone` only when a directory is inside a fleet checkout but should
be developed independently. Use `--file path/to/fleet.toml` when automatic
discovery cannot express the intended manifest.

## Move the fleet loop remotely

Add an explicit host without changing the selection model:

```bash
shinyhub dev . --remote dev
shinyhub dev . --app sales-dashboard --remote dev
```

Remote multi-app development verifies that every target already exists before
the first deployment, so a missing app causes no partial mutation. Creation is
intentionally single-app; see [Remote development](remote.md).

## Lower-level compatibility

The older fleet-local entry point remains available for scripts that need
`--check` or `--no-reload`:

```bash
shinyhub fleet dev sales-dashboard
shinyhub fleet dev sales-dashboard -f config/fleet.toml --check
```

`shinyhub run` remains standalone and intentionally does not discover or
compose fleet inputs.
