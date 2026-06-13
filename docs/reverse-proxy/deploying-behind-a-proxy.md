# Deploying from the CLI when ShinyHub is behind an auth proxy

When ShinyHub sits behind an authentication proxy (Authelia, oauth2-proxy,
Cloudflare Access, or any other forward-auth solution), browser users
authenticate at the edge before their requests ever reach ShinyHub. That flow
is browser-only: the proxy redirects unauthenticated requests to a login page
and exchanges cookies or tokens through the browser.

The CLI (`shinyhub deploy`, `shinyhub env`, `shinyhub apps`, and every other
subcommand) is not a browser. It cannot follow an interactive redirect, complete
an OAuth dance, or satisfy a CAPTCHA. A CLI request to the proxied hostname gets
bounced to the auth provider's login page and returns an unexpected HTML response
instead of the JSON the CLI expects.

## The fix: target the app port directly

For CLI and CI operations, bypass the proxy entirely. Talk directly to the
ShinyHub port (default `:8080`) and authenticate with a pre-shared deploy token.

```
Browser --> Proxy (auth) --> ShinyHub :8080
                                 ^
                                 |
                CLI / CI --------+  (bypasses the proxy; uses deploy token)
```

The two pieces are:

1. **Server side:** set `SHINYHUB_DEPLOY_TOKEN` (at least 32 characters) on the
   server process. Optionally set `SHINYHUB_DEPLOY_TOKEN_ROLE` to control what
   the synthetic deploy user can do (default: `developer`).

2. **Client side:** set `SHINYHUB_HOST` to the direct URL of the ShinyHub port
   (not the proxied hostname) and `SHINYHUB_TOKEN` to the same value you set for
   `SHINYHUB_DEPLOY_TOKEN`.

## Server configuration

In the environment of the `shinyhub serve` process:

```bash
SHINYHUB_DEPLOY_TOKEN="$(openssl rand -hex 32)"   # generate once; store in your secrets manager
SHINYHUB_DEPLOY_TOKEN_ROLE=developer               # optional; default is developer
```

The token is never persisted to disk. To rotate it, change the env var and
restart the server.

## Worked example

Assume ShinyHub listens on `10.0.1.5:8080` (reachable on your LAN or VPN), and
the proxied public hostname is `https://shiny.example.com`.

**Deploy an app:**

```bash
SHINYHUB_HOST=http://10.0.1.5:8080 \
SHINYHUB_TOKEN=your-deploy-token-here \
  shinyhub deploy ./my-app --slug myapp
```

**Set an environment variable:**

```bash
SHINYHUB_HOST=http://10.0.1.5:8080 \
SHINYHUB_TOKEN=your-deploy-token-here \
  shinyhub env set myapp KEY=value
```

The two env vars can also be written to the CLI credentials file
(`shinyhub login --host http://10.0.1.5:8080`), but for CI pipelines injecting
them as environment variables is simpler and avoids storing credentials on disk.

## CI pipeline example

```yaml
# GitHub Actions excerpt
- name: Deploy to ShinyHub
  env:
    SHINYHUB_HOST: http://10.0.1.5:8080
    SHINYHUB_TOKEN: ${{ secrets.SHINYHUB_DEPLOY_TOKEN }}
  run: shinyhub deploy . --slug myapp --wait
```

## Security notes

**Restrict direct access to the app port.** If `SHINYHUB_DEPLOY_TOKEN` is set,
anyone who can reach the app port and knows the token can deploy apps or change
settings. Restrict reachability to operators: bind the app port to a private
network interface, or protect it behind a firewall or VPN so only the proxy (for
browser traffic) and authorised operators (for CLI/CI) can connect.

**`trusted_proxies` and the direct CLI path.** `server.trusted_proxies`
controls which peer IP addresses may inject forward-auth identity headers
(`X-Forwarded-User`, etc.). A direct CLI connection from an address that is NOT
in `trusted_proxies` is treated as an ordinary authenticated client: the deploy
token is validated, but no forward-auth identity is accepted. This is the
correct behaviour - the CLI is not acting as a proxy and should not be able to
claim an arbitrary identity.

**App visibility and the app port.** When `defaults.app_visibility` is set to
`public` and the app port is directly reachable (LAN, VPN, or any other
interface), apps are accessible with no ShinyHub-level authentication from that
path. See the note in the configuration file and in the [App visibility and auth
proxy](#app-visibility-and-the-auth-proxy) section below for how to handle this.

## App visibility and the auth proxy

A common pattern behind an auth proxy is to set `defaults.app_visibility:
public` so the proxy handles all authentication and ShinyHub does not add a
second login prompt. This is safe only when the auth proxy is the EXCLUSIVE
ingress to ShinyHub.

If the ShinyHub app port is also reachable directly (LAN, VPN, Tailscale, or
a second network interface), `public` means apps are accessible with no auth at
all from that path - the proxy is bypassed and ShinyHub grants access freely.

In that case, prefer one of these approaches:

- Set `defaults.app_visibility: private`. Users who reach ShinyHub through the
  auth proxy still get a valid identity (forward-auth), so they are authenticated
  and see apps they have access to. Admins who connect directly with the deploy
  token can access everything. No app is exposed without auth.

- Keep `public` and firewall the app port so only the auth proxy host can reach
  it. Direct CLI access then goes through the proxy or via a side-channel (such
  as an SSH tunnel or a separate internal-only port).

## Config file vs. credentials file

ShinyHub has two distinct config concepts:

- **Server config file** (`shinyhub.yaml`): contains `auth`, `server`,
  `storage`, and other server settings. Selected with `shinyhub serve --config
  <file>` or the `SHINYHUB_CONFIG` env var on the SERVER process.

- **Client credentials file** (`~/.config/shinyhub/config.json`): stores the
  host URL and API token written by `shinyhub login`. Selected with the
  `--config` flag on CLIENT commands, or the `SHINYHUB_CONFIG` env var on the
  CLIENT side. When using `SHINYHUB_HOST` and `SHINYHUB_TOKEN` directly, the
  credentials file is not read.

The `SHINYHUB_CONFIG` name is shared by both roles (server config path on the
server, client credentials path on the client), which can be confusing. When
operating a server, set `SHINYHUB_CONFIG` in the server process environment. In
a CI job that runs only client commands, `SHINYHUB_CONFIG` points to the
credentials file - but for the token-based flow described in this guide,
`SHINYHUB_HOST` and `SHINYHUB_TOKEN` are simpler and do not require a file.
