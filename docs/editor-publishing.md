---
description: "Develop, check, preview, and publish from VS Code, Positron, or RStudio using the existing CLI."
---

# Editor publishing

The local editor packages wrap the same ShinyHub CLI used in terminals. They
select a saved server, run Doctor, and show a deployment plan before publishing.
The publishing action applies an exact saved bundle, so changes after review
cannot silently alter what is deployed. Concurrent server changes fail the
saved-plan precondition instead of overwriting another deployment.

Install the CLI with `uv tool install shinyhub` on the machine running the
editor integration. Both client and server must support saved-plan apply.

- **VS Code / Positron:** [local extension](../packaging/vscode/README.md).
  Commands appear in the command palette. Preflight and deployment run in
  dedicated task terminals. Workspace Trust is required; remote workspaces use
  a CLI installed on the remote host.
- **RStudio on macOS/Linux:** [local R package](../packaging/r-publish/README.md).
  Commands appear in the Addins menu. Preflight appears in the Console;
  development and deployment run in the Terminal.

Open a file in an app to select its nearest app root. In multi-root VS Code
workspaces, the extension asks for a folder when there is no active file.
Fleet users should select an app file for an individual publication; use
`shinyhub fleet apply` to reconcile a whole fleet.

Select **Connect to Server** (or **Connect to ShinyHub** in RStudio) for browser
sign-in. The integrations use the existing CLI credential store without
reading tokens themselves. A failed check or cancelled prompt stops publishing.
Temporary saved plans are cleaned up after apply or cancellation.
