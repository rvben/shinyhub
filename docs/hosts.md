# Working with several servers

The CLI keeps one credential per server. Connecting to a second ShinyHub adds it
alongside the first instead of replacing it, so a laptop can hold a local hub, a
staging server, and production at the same time and switch between them without
re-authenticating.

## Connect to a server

For a workstation, `connect` is the recommended first-time flow:

```bash
shinyhub connect https://shiny.example.com --name prod
```

It verifies the server and its runtimes, opens the browser for password or SSO
sign-in, asks you to approve a matching verification code, then saves a private
90-day credential. The raw credential is generated in the CLI and never passes
through the browser. A headless machine can use `--token-file`; CI can use
`SHINYHUB_HOST` and `SHINYHUB_TOKEN` without writing a credentials file.

Run `shinyhub login` when you want a short-lived username/password session or
need to refresh an existing session.

## Sign in to more than one server

```bash
shinyhub connect https://shiny.example.com --name prod
shinyhub connect http://localhost:8080 --name dev
```

`--name` is optional and gives the server a short alias. It has to be unique and
must not look like a URL, so `shinyhub use <name>` can never be a coin flip
between two servers. Reconnecting without `--name` keeps the alias the entry
already had.

The server you just connected to becomes the current one. The completion summary
names the authenticated identity, role, deploy permission, available runtimes,
credentials path, and previous server when the current selection changed.

Omit the URL to reconnect with the current server. Likewise, omit `--host` from
`login` to refresh a short-lived session for the current server.

## See what is saved, and switch

```bash
shinyhub hosts          # every saved server; * marks the current one
shinyhub use prod       # switch by name
shinyhub use https://shiny.example.com   # or by URL
```

Both commands read the local credentials file and contact no server, so they
still work when every server in the list is down - which is when "where am I
pointed?" is usually asked. `hosts` never prints a token, in any output format.

`shinyhub whoami` is the counterpart that does make a request: it asks the
current server who the saved credential authenticates as.

`shinyhub doctor --remote` goes further: it verifies the selected credential,
transport, server, identity, create-app permission, and reported runtimes. Add
`--slug sales` to check whether the identity can update that exact existing app
or create it if it is new. See [Doctor](doctor.md).

## Target one server for a single command

The global `--host` flag overrides the current server for one command:

```bash
shinyhub apps list --host prod
shinyhub deploy ./my-app --slug demo --host https://staging.example.com
```

The value is a saved server's name or a URL. `hosts` and `use` reject `--host`
rather than ignoring it: neither contacts a server, so a flag that appears to
scope them would be a lie.

## Signing out

```bash
shinyhub logout                 # the current server
shinyhub logout --host prod     # a specific saved server
shinyhub logout --all           # every saved server
```

Signing out of one server promotes another saved server to current and leaves
its credential alone. Each logout also asks that server to revoke the session,
best-effort: an unreachable server produces a warning, not a failure, and the
local credential is removed either way. `--all` attempts every revocation before
removing the file, so one unreachable server does not strand the others.

## Which credential a command uses

The server is chosen first, then that server's own token:

1. `--host` (a saved name or a URL)
2. `SHINYHUB_HOST`
3. the current host from the credentials file

The token comes from the entry for the resolved server. A token is never carried
over from a different server, so overriding the host cannot send production's
credential to whatever address a typo or an inherited environment variable
happens to name.

`SHINYHUB_TOKEN` is the way to say "use this credential, whatever the host":

```bash
SHINYHUB_HOST=https://shiny.example.com SHINYHUB_TOKEN=shk_... shinyhub apps list
```

That is the CI path - host and token from the environment, no credentials file
at all - and it is also how you deliberately reuse one credential against a
second URL for the same server (a direct IP behind a proxy, say).

## The credentials file

Written by `connect` or `login`, owner-readable only (`0600`), at
`~/.config/shinyhub/config.json` unless `--config`, `SHINYHUB_CREDENTIALS`, or
`SHINYHUB_CONFIG` points elsewhere.

```json
{
  "host": "https://shiny.example.com",
  "token": "…",
  "current_host": "https://shiny.example.com",
  "hosts": {
    "https://shiny.example.com": {
      "name": "prod",
      "token": "…",
      "user": "alice",
      "saved_at": "2026-01-05T09:12:44Z"
    },
    "http://localhost:8080": {
      "name": "dev",
      "token": "…",
      "user": "admin",
      "saved_at": "2026-01-05T09:14:02Z"
    }
  }
}
```

Server URLs are normalized before they become keys: the scheme and host are
lowercased and a trailing slash is dropped, so `HTTPS://Shiny.Example.com/` and
`https://shiny.example.com` are one entry rather than two. Any path keeps its
case, because a reverse-proxy subpath is case-sensitive.

The top-level `host` and `token` mirror the current entry. They exist so a CLI
older than multi-host support still finds a usable credential in this file. The
reverse is not recoverable: an old binary's `login` rewrites the file with only
those two fields and the other entries are gone, so upgrade the CLI everywhere
that shares a credentials file.

A file that cannot be parsed is an error rather than an empty store. "Never
logged in" and "the file was damaged" call for different responses, and only one
of them is fixed by logging in again.
