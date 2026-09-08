# ShinyHub for VS Code and Positron

Install the ShinyHub CLI on the machine running the extension (`uv tool install
shinyhub`), open an app folder, and run **ShinyHub: Connect to Server** from the
command palette. Browser sign-in uses the existing CLI credential store.

**ShinyHub: Publish App** selects a server, saves open files, runs Doctor, and
shows the deployment plan in a terminal. Deployment starts only after those
checks succeed and you select **Deploy**. The extension applies the exact saved
bundle you reviewed; subsequent source edits cannot change it, and concurrent
server changes fail the apply rather than being overwritten. The CLI follows
readiness and prints the app URL. A failed or cancelled check stops the flow.
If the server preserves the working version because deployment requires downtime,
the extension offers **Deploy with downtime** and explains that active sessions
will disconnect. Only that explicit choice retries the same saved bundle. A
stale plan instead asks you to publish again and review fresh server state.
Temporary plan files remain available during the choice and retry, and are
removed on completion or cancellation.

Other commands start local development, check readiness, or preview deployment.
The active file selects the nearest app directory within its workspace folder;
multi-root workspaces offer a folder chooser. For a fleet, select an app file to
publish that app; use the CLI's `fleet apply` for whole-fleet publishing.

The extension executes argument arrays directly, without a shell. It does not
read tokens or implement its own API client. It requires Workspace Trust and a
filesystem workspace. In remote editor sessions, install the CLI remotely and
use the terminal's printed URL if the remote machine cannot open a browser.

## Local development and packaging

Run `npm test` in this directory. Launch an Extension Development Host with
`code --extensionDevelopmentPath=/absolute/path/to/packaging/vscode`, or use the
equivalent Positron option. Package locally with `vsce package --no-dependencies`
if the VS Code packaging CLI is installed. This extension has no npm runtime
dependencies and has not been published to a marketplace.

From the repository root, `python3 scripts/test-editor-host.py` exercises real
extension activation and process-task execution in an isolated VS Code profile
using a fake CLI. It requires a graphical VS Code installation and a POSIX
shell, and does not access the user's ShinyHub credentials.
