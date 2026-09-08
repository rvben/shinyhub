---
description: "Check app isolation, producer support, and placement against the target server topology before uploading a deployment bundle."
---

# Runtime capability preflight

`shinyhub doctor .` checks the selected app's placement and the isolation mode
declared in its manifest before a bundle is uploaded. It rejects declared
deploy-triggered producers and `on_success = "roll"` when the target topology
cannot support them. The `runtime-topology` check includes the server's reason
and a concrete remedy in both terminal and JSON output.

For a new app, the check uses the server's default tier and isolation. For an
existing app, it uses its actual placement. An explicit manifest isolation
overrides the stored mode for this read-only check. Older servers that do not
advertise `runtime_capabilities` retain the previous Doctor behavior.

For container and worker placements, Doctor no longer rejects a deployment
solely because the control-plane host lacks `uv` or `Rscript`. It explains that
runtime dependencies must be available in the target image or worker. Native
placements still check host launchers. Mixed placements check host launchers
when any replica uses the local native runtime.

## Current support

| Capability | Supported topology |
|---|---|
| Multiplex workers | Single-node and clustered control planes |
| Grouped/per-session workers | Single-node control plane |
| Deploy-triggered producers | Local native tiers, multiplex workers, cleared orphan fence |
| Automatic serving-data activation (`on_success = "roll"`) | Single-node, local native tiers, multiplex workers, cleared orphan fence |

This is a topology check, not a promise of available capacity or an upgrade to
distributed workers. Resource limits, worker budgets, readiness, scheduling,
and publication fencing are still enforced at the deployment boundary. Plain
scheduled jobs do not require producer/activation support.

## API

Authenticated callers can read `GET /api/runtime-capabilities` for server
defaults. App managers can read `GET /api/apps/{slug}/capabilities` for an app.
The optional `?isolation=multiplex|grouped|per_session` projects a proposed mode.
Neither endpoint changes state. Other app viewers cannot inspect this surface.

The response includes `isolation`, `clustered`, `tiers`,
`requires_host_runtime`, and `features`. Each
feature has `supported`; unavailable features also include `reason` and
`remedy`. Feature names are `multiplex`, `grouped`, `per_session`,
`deploy_producers`, and `data_activation`. The API calls the same topology
validators as schedule writes, preserving fail-closed orphan fencing.

For topology changes, see [worker isolation](isolation.md),
[scheduled jobs](schedules.md), and [clustered HA](deployment/ha-data-plane.md).
