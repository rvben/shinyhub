---
description: "Deploy and operate interactive R and Python applications with authentication, scaling, hibernation, and observability, on hardware you control."
hide:
  - navigation
  - toc
---

<section class="shiny-hero" markdown>

<div class="shiny-hero__copy" markdown>

<span class="shiny-status"><span aria-hidden="true"></span> Self-hosted · R and Python</span>

# Develop locally. Deploy with confidence.

ShinyHub gives authors of R Shiny, Python Shiny, Dash, and Streamlit apps one
safe development command, then deploys and operates the same applications with
built-in authentication, scaling, hibernation, and observability.

<div class="shiny-actions" markdown>

[Develop an app](local-development.md){ .shiny-button .shiny-button--primary }
[Try ShinyHub locally](getting-started/quickstart.md){ .shiny-button .shiny-button--secondary }

</div>

```bash
shinyhub dev .
```

Your directory chooses the scope. Add `--remote <host>` only when the app needs
that host. Source stays untouched, and only healthy edits reach the browser.

</div>

<figure class="shiny-hero__visual">
  <a href="https://demo.shinyhub.dev" aria-label="Open the live ShinyHub demo"><img src="images/dashboard.jpg" alt="ShinyHub Demo launchpad grouping live identity, Python Shiny, R Shiny, Plotly Dash, and Streamlit applications"></a>
  <figcaption><span aria-hidden="true"></span> Five live apps. One calm control plane.</figcaption>
</figure>

</section>

<div class="shiny-proof" role="list">
  <div role="listitem"><strong>One dev command</strong><span>Use the same loop for one app, a fleet, or a remote host.</span></div>
  <div role="listitem"><strong>Safe reloads</strong><span>Broken edits never replace the last healthy version.</span></div>
  <div role="listitem"><strong>Production path</strong><span>Develop through the same route and bundle model you deploy.</span></div>
</div>

## From source to a dependable URL

<div class="shiny-journey" markdown>

1. **Develop safely**

   `shinyhub dev .` runs the production-shaped route and swaps in an edit only after it becomes healthy.

2. **Preview the change**

   `shinyhub plan` shows the exact archive, manifest effects, permissions, and lifecycle changes without mutating the server.

3. **Deploy with progress**

   Follow dependency preparation, replica readiness, routing, and recovery through one stable CLI workflow.

4. **Operate by outcome**

   Keep viewer sessions responsive with hibernation, render pacing, worker isolation, metrics, and tracing.

</div>

```bash
shinyhub dev ./my-app  # edit safely; Ctrl-C when ready
shinyhub connect https://hub.example.com --name prod
shinyhub doctor ./my-app
shinyhub plan ./my-app
shinyhub deploy ./my-app --open
```

<section class="shiny-split" markdown>

<div markdown>

## Start small, keep the path forward

Use the same Go binary for a local evaluation, a single Docker host, or a PostgreSQL-backed multi-node deployment. Runtime tiers let one control plane place applications locally, on remote Docker workers, or on AWS Fargate.

[Choose an installation →](getting-started/installation.md)

</div>

<div class="shiny-terminal" aria-label="Example application manifest" markdown>

```toml
[app]
name = "Revenue forecast"
replicas = 2
max_sessions_per_replica = 10
render_seconds = 1.3
```

</div>

</section>

## Built for everyone in the delivery chain

Developers get one continuous path from the first local edit to a traced remote
deployment. Operators get explicit state, bounded resources, and recovery
paths. Viewers get the part that matters: a dashboard that opens quickly and
stays dependable.

## Where ShinyHub fits

ShinyHub is for a team that has a Linux host and a handful of Shiny, Dash, or
Streamlit applications, and wants them behind real URLs with logins, deploys,
logs, and an audit trail, without running a platform to get there. The
comparison is usually against Posit Connect, ShinyProxy, or a Docker Compose
setup someone assembled by hand, and it comes down to a few axes.

- **Cost and licensing.** MIT, self-hosted, no per-user or per-application
  license.
- **What you have to operate.** One Go binary and one SQLite file. No JVM, no
  Kubernetes, no broker, and no database server to run. Postgres is optional
  and only for [multi-instance HA](deployment/ha-data-plane.md).
- **How apps run.** As ordinary host processes by default. A container per
  application is opt-in (`runtime.mode: docker`), a container per browser
  session is never required, and idle applications hibernate instead of
  holding memory.
- **What you assemble yourself.** Nothing for the common path: routing, login
  (OAuth, OIDC, or an auth proxy), per-app access control, logs, metrics, and
  deployment history are in the box.

Where it is deliberately not the answer: the native runtime is not a security
boundary between tenants who do not trust each other. Use the Docker runtime or
separate hosts for that, and read
[Do not run mutually-untrusting tenants on the native runtime](isolation.md#do-not-run-mutually-untrusting-tenants-on-the-native-runtime)
before deciding. For what the host itself needs, see
[What it needs to run](getting-started/installation.md#what-it-needs-to-run).

<div class="shiny-final" markdown>

### See a real ShinyHub instance

The public demo runs curated applications on an isolated app origin. Open an app anonymously, or enter with one click for a read-only tour of the actual control plane.

[Open the demo guide](demo.md){ .shiny-button .shiny-button--primary }

</div>
