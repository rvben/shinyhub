---
hide:
  - navigation
  - toc
---

<section class="shiny-hero" markdown>

<div class="shiny-hero__copy" markdown>

<span class="shiny-status"><span aria-hidden="true"></span> Self-hosted · R and Python</span>

# Fast dashboards. Calm operations.

ShinyHub deploys and operates interactive R and Python applications with built-in authentication, scaling, hibernation, and observability—without turning your platform into another cloud console.

<div class="shiny-actions" markdown>

[Start locally](getting-started/quickstart.md){ .shiny-button .shiny-button--primary }
[Try the live demo](demo.md){ .shiny-button .shiny-button--secondary }

</div>

```bash
uvx shinyhub serve
```

One command starts the real server, opens the dashboard, and deploys a live
Python Shiny example on the first run. No account or throwaway demo mode.

</div>

<figure class="shiny-hero__visual">
  <a href="https://demo.shinyhub.dev" aria-label="Open the live ShinyHub demo"><img src="images/dashboard.jpg" alt="ShinyHub Demo launchpad grouping live identity, Python Shiny, R Shiny, Plotly Dash, and Streamlit applications"></a>
  <figcaption><span aria-hidden="true"></span> Five live apps. One calm control plane.</figcaption>
</figure>

</section>

<div class="shiny-proof" role="list">
  <div role="listitem"><strong>One binary</strong><span>Start with SQLite and no external services.</span></div>
  <div role="listitem"><strong>Several runtimes</strong><span>Run R Shiny, Python Shiny, Dash, and Streamlit.</span></div>
  <div role="listitem"><strong>Production controls</strong><span>Authentication, isolation, scaling, and audit history.</span></div>
</div>

## From source to a dependable URL

<div class="shiny-journey" markdown>

1. **Validate locally**

   Run the production-shaped app route on your workstation and let `doctor` find bundle, runtime, and access problems before deployment.

2. **Preview the change**

   `shinyhub plan` shows the exact archive, manifest effects, permissions, and lifecycle changes without mutating the server.

3. **Deploy with progress**

   Follow dependency preparation, replica readiness, routing, and recovery through one stable CLI workflow.

4. **Operate by outcome**

   Keep viewer sessions responsive with hibernation, render pacing, worker isolation, metrics, and tracing.

</div>

```bash
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

Developers get a direct deployment workflow. Operators get explicit state, bounded resources, and recovery paths. Viewers get the part that matters: a dashboard that opens quickly and stays dependable.

<div class="shiny-final" markdown>

### See a real ShinyHub instance

The public demo runs curated applications on an isolated app origin. Open an app anonymously, or enter with one click for a read-only tour of the actual control plane.

[Open the demo guide](demo.md){ .shiny-button .shiny-button--primary }

</div>
