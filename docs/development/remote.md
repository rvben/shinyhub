---
description: "Continuously deploy healthy local changes to an explicit ShinyHub development host with safe targets and durable logs, traces, and deployment history."
---

# Remote development

Move the development loop to a remote host when an app depends on that host's
data, identity, network, runtime, or compute. First save the host:

```bash
shinyhub connect https://hub.example.com --name dev
```

Then attach the current directory to an existing app:

```bash
shinyhub dev . --remote dev --slug sales-dev --open
```

<div class="shiny-safety-contract" markdown>

**Remote development is safe by default**

- Nothing remote happens unless `--remote <host>` is present.
- The default is attach-only: the target must already exist.
- Multi-app sessions verify every target before changing any target.
- Failed candidates retain their reason and attempt to restore the last
  successful release.
- Every attempted save is durable and traceable in the Deployments view.

</div>

The saved current host and `SHINYHUB_HOST` never turn an ordinary local `dev`
invocation into a remote mutation.

## Choose the target lifecycle

### Attach an existing app — default

```bash
shinyhub dev . --remote dev --slug sales-dev
```

A misspelled slug fails instead of creating a durable app.

### Create a persistent app

```bash
shinyhub dev . --remote dev --create --slug sales-dev
```

`--create` fails if the slug already exists. The app remains after the watch
process stops.

### Create a private scratch app

```bash
shinyhub dev . --remote dev --ephemeral --ttl 8h
```

Ephemeral apps are always private and use a recognizable
`<directory>-dev-<suffix>` slug when none is provided. The TTL must be between
15 minutes and seven days. Closing the terminal ends the development session,
but the app remains available until its printed expiry, so a shared URL does
not disappear unexpectedly.

## Use fleet scope

Fleet discovery and selection work exactly as they do locally:

```bash
# Preflight and attach every local-source app in the fleet.
shinyhub dev . --remote dev

# Attach one app and compose its shared inputs.
shinyhub dev . --app sales-dashboard --remote dev

# Creation remains deliberately single-app.
shinyhub dev . --app sales-dashboard --remote dev --create
shinyhub dev . --app sales-dashboard --remote dev --ephemeral --ttl 8h
```

A multi-app session verifies that every selected target exists before the first
deployment. If any target is missing, nothing is changed and the CLI points to
`shinyhub fleet apply`. Each app receives its own durable development-session
ID and Deployments group. Terminal output is prefixed with the app slug, and
multi-app NDJSON records include an `app` field.

## What happens after a save

ShinyHub waits for a 750 ms quiet period, builds the same canonical bundle as
ordinary `deploy`, and sends only the latest state. Changes arriving during a
deployment are coalesced into one follow-up attempt. Generated, ignored, or
metadata-only changes with the same content digest cause no remote mutation.

A build, hook, startup, or readiness failure is printed without ending the
watch process. Fix the source and save again. The server attempts to restore
the previous successful release after a failed candidate.

Remote deployments currently cycle the app pool during handoff, so viewers may
see a brief interruption after each deployed change. Use a dedicated
development slug when that interruption is unacceptable.

## Deployment history, logs, and traces

Every attempted change is an ordinary durable deployment row. Successful
versions remain rollback-capable, and failed attempts retain their failure
reason. A generated development-session ID connects deployments, replica logs,
and proxy traces.

The Deployments tab presents one compact **Remote development** item per watch
process, including actor, target type, save and failure counts, current release,
and session state. Expand it for the complete attempt history. **View logs** and
**View traces** open observability already filtered to that session.

A heartbeat renews the session independently of saves, so **Last save** remains
the latest deployment attempt rather than a liveness signal. Clean shutdown
ends the session immediately; after a killed CLI or lost connection, the
server-owned lease closes it automatically.

## Options and safeguards

```bash
shinyhub dev . --remote dev --slug sales-dev
shinyhub dev . --remote dev --create --slug sales-dev
shinyhub dev . --remote dev --ephemeral --ttl 8h
shinyhub dev . --remote dev --watch-delay 2s
shinyhub dev . --remote dev --allow-repeated-hooks
shinyhub dev . --remote dev --output ndjson
```

`--create` and `--ephemeral` are mutually exclusive. `--watch-delay` accepts
100 ms through one minute. Because post-deploy hooks can have non-idempotent
side effects, watch mode refuses a manifest containing hooks unless you add
`--allow-repeated-hooks` after reviewing them.

A continuous command cannot emit one final JSON document; use NDJSON for
automation. Press <kbd>Ctrl</kbd>+<kbd>C</kbd> to stop watching. The last
successful remote deployment remains in place.

`shinyhub deploy --watch` remains available for scripts and specialist options,
but new interactive workflows should use `shinyhub dev` so local and remote
development share one mental model.
