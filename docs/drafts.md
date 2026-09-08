---
description: "Retain a deployment bundle, review it in a private preview with separate credentials and data, and promote that exact source archive to production."
---

# Review deployment drafts

A draft retains the uploaded source archive without replacing the production
app. Review it in a separate private preview, then promote the retained archive.
Editing the local source after upload does not change the draft.

The production app must already exist. Drafts last 24 hours by default; use
`--draft-ttl` to choose between 15 minutes and 168 hours. An app can retain at
most 20 drafts. An older server without draft support rejects the request;
the CLI never falls back to a production deployment.

```bash
shinyhub deploy . --slug sales --draft --open
# Prints the draft ID and starts a private preview.

shinyhub drafts list sales
shinyhub drafts preview sales <draft-id> --open

# Grant a colleague viewer access to this preview only.
shinyhub apps access grant <preview-slug> <username>

# After reviewing the preview:
shinyhub drafts promote sales <draft-id>
shinyhub drafts delete sales <draft-id>
```

Omit `--open` to retain the upload without starting any application. The printed
`drafts preview` command starts it later. JSON output includes `id`,
`content_digest`, `preview_slug` once created, and Unix-second `created_at` and
`expires_at` timestamps.

## Preview environment

Previews appear as **Draft: sales** in the app catalog and have their own
`/app/preview-<draft-id>/` URL. They are private even if production is public.
The creator owns the preview; platform administrators and operators retain
their normal access. Production viewer and group grants are not copied.
Reviewers use ordinary viewer grants on the preview and cannot promote it.
Promotion requires management permission on the production app.

| Setting | Preview behavior |
|---|---|
| Source archive | Exact retained upload; digest checked before execution and promotion |
| Execution policy | Copies production replica placement, replica count, worker policy, resource limits, session cap, hibernation timeout and render pacing when the preview is created; bundle settings then apply normally |
| Environment and secrets | Separate; production app variables are not copied |
| Writable data | Separate app data directory; production files and shared mounts are not copied |
| Access | Private; grant preview reviewers explicitly |
| Code and visibility | Cannot be replaced or made public through ordinary app endpoints |
| Expiry | Promotion stops at the deadline; the existing owner-controlled reaper removes preview processes and retained archives, normally on its next one-minute pass |

Apps still receive the runtime's normal server-level environment. A preview is
an independent app, not an additional security sandbox: the configured native
or container isolation model still applies. Apps with embedded external
credentials or absolute file paths can still reach those resources.

If startup needs credentials or sample data, the failed preview reports its
slug and stays available for configuration:

```bash
shinyhub env set <preview-slug> DATASET=sample
shinyhub data push <preview-slug> ./sample.csv
shinyhub drafts preview sales <draft-id> --open
```

Use the usual secret-input options for sensitive values. Check the preview's
Logs tab or `shinyhub apps logs <preview-slug> --no-follow` for startup failures.

## Promotion guarantees and boundaries

Promotion requires a successful deployment of the same digest to the preview.
It checks the retained archive's digest again, then sends it through the normal
production deployment pipeline with a captured production revision precondition.
A changed production revision rejects promotion before changing the running app;
upload a fresh draft against the new baseline and review it again. The revision
includes operational settings and state, so changes such as stopping or
hibernating production may also require a new draft.

The archive is the reviewed **source bundle**, not a prebuilt runtime image.
Production dependencies are prepared normally. Use dependency lockfiles to
make those builds reproducible. Preview environment variables, credentials and
data are not promoted; production retains its own values.

The existing availability rules apply. A stopped production app stays stopped
unless `drafts promote --start` is requested. When parallel handoff is unsupported,
promotion refuses with the working version preserved; add `--allow-downtime`
only when a stop-first deployment is intended. A successful promotion cannot be
repeated using the same draft. Normal deployment history provides rollback.

The initial preview implementation rejects bundles containing post-deploy
hooks, schedules, or access-group declarations before executing any code.
These declarations may have external side effects or broaden preview access.
They are never silently stripped from the reviewed archive. Use a separate
development app for those workflows for now.

Deleting a draft removes its preview and retained archive without deleting the
production deployment. Expired drafts are removed automatically after their
preview has been reaped. Draft archives live inside the production app's storage
tree, count against its disk quota, and are included by normal storage backups.

## API

All endpoints require management permission on the production app:

| Method and path | Effect |
|---|---|
| `POST /api/apps/{slug}/drafts?ttl=24h` | Multipart `bundle` upload; returns the draft with HTTP 201 |
| `GET /api/apps/{slug}/drafts` | Returns `{ "items": [...] }`, newest first, at most 100 entries |
| `POST /api/apps/{slug}/drafts/{id}/preview` | Creates and deploys a private preview, or returns the existing successful preview |
| `POST /api/apps/{slug}/drafts/{id}/promote` | Promotes the retained upload through the normal deployment pipeline |
| `DELETE /api/apps/{slug}/drafts/{id}` | Removes the preview and retained upload |

Promotion accepts `?start=true` and the normal
`X-ShinyHub-Allow-Downtime: 1` header. HTTP 409 reports a conflicting production
revision, missing successful preview, changed archive, or repeat promotion;
HTTP 410 reports an expired draft. Preview startup errors include the preview
slug in `X-Shinyhub-Draft-Preview` so clients can direct users to configuration
and logs. Mutations record `draft_create`, `draft_preview_create`,
`draft_promote`, and `draft_delete` audit events, alongside normal deployment
and ephemeral-app lifecycle events.
