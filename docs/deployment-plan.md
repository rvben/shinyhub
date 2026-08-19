---
description: "Use plan and apply when a deploy needs review, an audit trail, or a strict split between approving the bundle and shipping it."
---

# Plan and apply

ShinyHub has two deployment paths:

- `shinyhub deploy` is the fast path. It prepares the current source and deploys
  it immediately.
- `shinyhub plan --out` followed by `shinyhub apply` is the reviewable path. It
  saves the exact bundle, desired state, target, expiry, and remote revision that
  were reviewed, then applies those bytes without rebuilding from the working
  tree.

Use the exact path when a deployment needs human approval, an audit trail, or a
strict separation between review and execution:

```bash
shinyhub doctor .
shinyhub plan . --out sales.plan
shinyhub plan show sales.plan
shinyhub apply sales.plan
```

Pass the source path explicitly. Plan, like deploy, never assumes that the
current directory is safe to bundle.

## Read a plan

Human output follows the same decision order for a single app and a fleet:

1. outcome;
2. user, availability, ownership, or destructive impact;
3. semantic changes;
4. bundle or resource detail;
5. create/update/adopt/delete totals;
6. the safest next command.

At narrow terminal widths the layout stacks instead of discarding information.
Color is additive: action words, symbols, headings, and ordering retain the same
meaning with `--no-color`, `NO_COLOR`, or ASCII-only output.

The default view is decision-sized. Ask for implementation detail only when it
is useful:

```bash
shinyhub plan . --details
shinyhub plan show --details sales.plan
shinyhub plan show --files sales.plan
```

The detail view includes every bundled path, launch command, readiness contract,
manifest effect, permission, target URL, and saved-plan integrity field. JSON is
always complete and does not apply the human view's progressive disclosure.

## What plan verifies

`shinyhub plan` uses the same bundle preparation, launch resolution, manifest
validation, and target selection as deploy. It reports:

- create, update, or unchanged/redeploy intent;
- current and planned content digests and typed configuration values;
- create/manage permission, access, ownership, and lifecycle effects;
- source size, upload size, included paths, ignore rules, and protected data or
  cache paths;
- runtime, dependency preparation, launch command, readiness endpoint, and
  startup deadline;
- hooks, schedules, access declarations, tracing, and other manifest effects;
- fleet adoption and every prune candidate in an isolated destructive section;
- degraded comparisons, version skew, in-progress deployment, ownership, and
  availability warnings.

Planning is read-only. It performs local preparation and remote reads, but never
creates an app, uploads a bundle, changes access, starts a stopped app, or probes
permission with a write.

An unchanged digest means the bundle's files, executable bits, and manifest
match the newest successful deployment. Applying is still explicit because a
redeploy may replace replicas. If an older server cannot report a live digest,
plan reports the comparison as `unknown`; it never invents equality.

## Saved plans are exact and private

`--out` writes an owner-only (`0600`) plan container containing application
source. Treat it like a release artifact:

- do not commit it;
- transfer and retain it only where the source itself is allowed;
- use `--expires-in` to shorten the default 24-hour lifetime;
- use `--force` only when deliberately replacing an existing plan file.

Before any mutation, `shinyhub apply` verifies the container structure,
integrity digest, embedded bundle digest, expiry, target host, desired-state
consistency, server compatibility, and the target's resource revision. A
changed working tree is irrelevant: apply uploads the reviewed embedded bundle.

If the app changed after planning, apply returns a conflict and tells you to
create a new plan. It does not silently recompute or apply against the newer
state. Creates use an expected-absent precondition, so another actor claiming
the slug is also rejected before deployment.

Use `plan show` for offline inspection. It verifies the artifact but permits an
expired plan because inspection cannot mutate the server; `apply` never permits
an expired plan.

## Fleet plan and apply

Fleet planning uses the same action vocabulary and count model:

```bash
shinyhub fleet plan -f fleets/eu.toml
shinyhub fleet apply -f fleets/eu.toml
```

Ownership transfers require `--adopt`. Deletion requires `--prune` plus an
interactive confirmation, or an explicit `--yes` supplied by the operator.
Suggested and recovery commands intentionally never add `--yes`.

Fleet apply is non-atomic and continues across resources. Its report therefore
records a terminal status and a separate mutation state for every app or
project:

- `none`: ShinyHub can prove no mutation occurred;
- `committed`: the requested resource change completed;
- `partial`: a mutation committed but a later convergence step failed;
- `unknown`: the client cannot prove whether the remote mutation committed.

Every run has a cryptographically random run ID for correlation with server
audit records. On failure, human and JSON output identify committed, partial,
and unknown resources and provide one of three recovery strategies:

- `resume`: transient failure; re-run apply;
- `repair_then_resume`: fix the deterministic or post-commit failure, then
  re-run apply;
- `replan`: remote state conflicted; review a fresh plan before re-applying.

Re-running fleet apply recomputes current state. Already completed resources
become unchanged, so they are not applied twice. Deploy attempts automatically
retry only readiness timeouts, transport failures, and server errors; config
patches retry only server-side failures. Invalid bundles, missing runtimes,
build/hook failures, validation errors, conflicts, and crashes are not repeated
implicitly.

Older servers without fleet preconditions remain usable in a clearly marked
degraded mode. Config reconciliation narrows the race with a fresh read, while
pruning is disabled unless the operator explicitly accepts the risk with
`--allow-unsafe-degraded-prune`.

## Automation contract

Request JSON explicitly instead of parsing terminal text:

```bash
shinyhub plan . --output json
shinyhub plan . --detailed-exitcode --output json
shinyhub fleet plan -f fleets/eu.toml --output json
shinyhub fleet apply -f fleets/eu.toml --output json
```

The plan envelope includes a schema version, typed resources and values,
impacts, warnings, counts, and next actions. Single-app output also includes the
bundle, launch, manifest, remote state, and deploy command. Fleet apply adds the
run ID, run status, per-resource result and mutation state, summary exit fields,
and structured recovery guidance.

Plan exit codes:

| Code | Meaning |
|---:|---|
| `0` | Plan printed; with detailed exit codes, content is unchanged |
| `1` | Local validation or CLI/server protocol compatibility failed |
| `2` | Detailed mode only: content is new, changed, or cannot be compared |
| `3` | Network, authentication, or authorization failed |
| `6` | The host answered, but ShinyHub was not ready |

Fleet apply additionally uses `4` for partial convergence and `5` for a remote
precondition conflict. A successfully printed ordinary plan returns `0` unless
`--detailed-exitcode` (or its `--fail-on-changes` alias) was requested.

## Fast path

For an interactive deployment where a separately approved artifact is not
needed:

```bash
shinyhub doctor .
shinyhub plan .
shinyhub deploy . --open
```

`--open` waits for health, verifies the routed app when possible, and opens its
canonical URL. The plan's final command remains copy-pasteable and uses `--wait`
for automation; replace it with `--open` for the browser handoff.
