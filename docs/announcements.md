---
description: "Schedule public platform notices for maintenance and interruptions, including users already working inside hosted apps."
---

# Platform announcements

Administrators can open **Announcements** in the sidebar to create a platform-wide notice. Notices appear on the sign-in page, launchpad, console, and supported top-level hosted app pages. Changes do not require a server restart.

## Create and publish a notice

1. Choose **New announcement**.
2. Enter a title (up to 120 characters) and message (up to 600 characters). Describe the event, the expected impact, and what users should do. Include the maintenance date, time, and timezone in the message.
3. Optionally add an absolute HTTP or HTTPS details link.
4. Choose **Information**, **Warning**, or **Critical**, and whether users may dismiss the notice. Selecting Critical defaults to non-dismissible.
5. Inspect the preview, then **Save draft**, **Publish now**, or choose a future **Show from** time and **Schedule announcement**.

The visibility window controls when the notice appears, not when maintenance happens. To announce Saturday maintenance on Wednesday, use Wednesday as **Show from** and describe Saturday's maintenance window in the message. Times are entered in the browser's timezone; the editor also shows the exact UTC timestamps. Set **Show until** for automatic expiry, or leave it empty for an ongoing notice.

Announcement content is public, including before sign-in. Do not include private operational details or personal data. Drafts, scheduled future content, and creator/updater metadata are excluded from the public feed.

## Edit and withdraw

**Save changes** updates the notice in place. A content, severity, visibility, or dismissal change creates a new display revision, so users who dismissed the old version can see the update. Dismissal is remembered in that browser and origin, not across devices or between an isolated app origin and the console.

**Disable** withdraws a notice immediately from server responses. **Archive** retains it in management history and prevents republication. Expired notices remain available through **All announcements**. Concurrent edits are rejected; the editor preserves your work and offers **Reload saved version**.

When multiple notices are active, Critical comes first, then Warning, then Information; newer publication breaks ties. **View all announcements** expands the other active notices.

Successful mutations and their audit records commit together. Only platform-wide administrators may manage notices. App-scoped administrator credentials cannot do so. An unrestricted administrator service credential can use the API for automation.

For persistent instance identity, use [branding.environment](branding.md#environment-identity).
Its strip has a separate slot and is never hidden by announcement ordering.

## Delivery behavior and limitations

Visible, connected pages refresh approximately every 30 seconds and at publication boundaries, with a refresh on returning to a tab. Publication, editing, and withdrawal normally reach an already-open page within 45 seconds. Background tabs refresh when they become visible. Requests have a timeout and bounded retry backoff; a failed read keeps the last known notice until its scheduled expiry.

Hosted app notices use optional, isolated platform chrome and work independently of the app switcher. The support-session safety rail takes precedence. The announcement endpoint is served directly by ShinyHub, so polling does not wake a sleeping app.

Injection follows the existing bounded HTML and CSP rules. Unsupported or restrictive responses pass through without a notice; they never become inaccessible because of an announcement. Embedded iframe instances omit the injected rail. An app tab opened before upgrading to the release that introduced announcements needs one reload to receive the client.

Notices communicate maintenance; they do not stop apps, drain sessions, guarantee readership, or send email. They also cannot substitute for an external status page while ShinyHub is unreachable.

## Release pipeline CLI

Use `shinyhub announcements` with platform-wide administrator credentials. The example below requires Bash and `jq`. The CLI uses the existing `SHINYHUB_HOST` and `SHINYHUB_TOKEN` environment variables or saved client credentials. App-scoped credentials cannot manage announcements.

```bash
set -euo pipefail

# Publish before the first disruptive step; keep this ID as a pipeline artifact
# together with its revision if publication and withdrawal run in separate jobs.
notice=$(shinyhub announcements publish --severity warning --ttl 60m \
  --title "Release in progress" \
  --message "Dashboards may briefly disconnect; reload the page if one stops responding." \
  --output json)
notice_id=$(jq -er '.id' <<<"$notice")
notice_revision=$(jq -er '.announcement.revision' <<<"$notice")

# Allow connected, visible pages time to fetch the notice before disruption.
sleep 45

# Run the host upgrade, app rollout, and smoke tests here.
# Only withdraw after those steps succeed:
shinyhub announcements disable "$notice_id" --expected-revision "$notice_revision"
```

`publish` requires exactly one of a positive `--ttl` (for example `60m`) or a future `--ends-at` RFC3339 timestamp with an explicit timezone. Expiry is calculated using the CLI machine's clock; keep pipeline and server clocks synchronized and allow sufficient time for the release. Severity defaults to `information`. Critical notices default to non-dismissible; override with `--dismissible=true`. Use `--details-url` for an optional public details link. JSON publication output contains `status`, `id`, and the saved record under `announcement`.

Inspect a notice with `shinyhub announcements get "$notice_id"`. `shinyhub announcements list` traverses all history pages, then applies the standard `--limit`, `--offset`, and `--fields` controls. JSON list output uses the standard `items`, `total`, `limit`, and `offset` envelope. Concurrent changes to history can affect pagination; this is not a transactional snapshot.

`disable` fetches the latest saved revision and protects against edits between that read and the write. Without `--expected-revision`, it also withdraws edits made after publication. Pipelines should pass the revision returned by `publish` using `--expected-revision`; a later edit then returns a conflict (exit 5) without withdrawal. Already disabled or archived records succeed without a write. A concurrent edit returns a conflict; inspect the record before explicitly retrying. Each release should retain its own notice ID so an older release cannot clear a newer release's notice. Writes are not automatically retried. Transport failures, service/proxy errors, and malformed success responses after a write return `internal` with exit 1 and an outcome-unknown hint, rather than a retryable exit 3. Publication or withdrawal may already have succeeded: inspect history or the record before retrying. HTTP 4xx rejections retain the standard error kinds and exit codes.

The 45-second delay is a best-effort propagation window, not a readership guarantee. Background tabs, pages that have not fetched the notice, and new or reloaded pages during an outage cannot rely on the retained notice. Feed failures retain previously fetched notices in the current page until expiry; that cache does not persist across a page reload.

Do not withdraw unconditionally in an exit trap: a failed release may leave users experiencing disruption. Retain its notice until expiry, or use the administration UI/API to describe the remaining impact. TTL expiry withdraws stale messaging; it does not indicate recovery. The outer release pipeline owns the full window, including host/auth restarts and smoke tests; `fleet apply` does not publish or clear notices.

## API

Public active feeds:

```text
GET /api/announcements/active
GET /app/{slug}/.shinyhub/announcements.json
```

Both return `announcements`, `server_time`, and `next_transition`. The app-origin feed is read-only and has the same public content regardless of the slug. All feeds use `Cache-Control: no-store`.

Management endpoints require platform-wide administrator credentials:

```text
GET   /api/announcements?limit=50&offset=0
POST  /api/announcements
GET   /api/announcements/{id}
PATCH /api/announcements/{id}
```

The list returns `announcements` and `has_more`, with a maximum page size of 100. Records include derived `status`, `revision`, and `display_revision`. Publication is one of `draft`, `published`, `disabled`, or `archived`; published records become scheduled, active, or expired according to their UTC dates.

Create an immediately active notice:

```json
{
  "title": "Scheduled maintenance",
  "message": "3 October, 22:00–22:30 CEST. Apps may disconnect. Save your work before 22:00.",
  "severity": "warning",
  "publication": "published",
  "dismissible": true,
  "ends_at": "2026-10-03T20:30:00Z"
}
```

Omitting `starts_at` on publication uses the current server time. Omitted publication defaults to `draft`. Dates use RFC3339 with an explicit timezone; `null` clears an optional date.

PATCH changes only supplied fields and requires the record's current `expected_revision`:

```json
{
  "expected_revision": 3,
  "publication": "disabled"
}
```

A stale revision returns `409 Conflict`. Invalid content or dates return `400 Bad Request`. Cookie-authenticated mutations use the existing CSRF protection. Updates do not require a timer worker: visibility is derived from durable publication records on every cache refresh, including after restart or control-plane failover.
