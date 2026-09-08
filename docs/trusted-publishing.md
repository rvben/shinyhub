---
description: "Exchange a trusted CI workload identity for an app-scoped, ten-minute deployment credential without storing a ShinyHub API key."
---

# Trusted publishing

Trusted publishing lets a configured CI workload obtain a short-lived service
credential without a stored ShinyHub API key. `shinyhub ci` exchanges the
workload identity and runs one CLI command with the temporary credential.

The administrator declares exact trust conditions in `shinyhub.yaml`. With no
policies, the exchange endpoint is disabled and the server advertises
`trusted_publishing: false`.

```yaml
auth:
  trusted_publishers:
    - name: production
      issuer: https://token.actions.githubusercontent.com
      jwks_url: https://token.actions.githubusercontent.com/.well-known/jwks
      audience: https://hub.example.com
      subject: repo:example/analytics:ref:refs/heads/main
      claims:
        repository_id: "123456"
        repository_owner_id: "987654"
        ref: refs/heads/main
        workflow_ref: example/analytics/.github/workflows/deploy.yml@refs/heads/main
      apps: [sales-dashboard]
```

Replace the example identifiers with the repository's real immutable IDs and
the exact signed subject. GitHub repositories using immutable subjects include
owner and repository IDs in `sub`; copy the format your repository uses.
There are no wildcard or regular-expression matches.
GitHub policies require repository and owner IDs, ref, and workflow ref, and
use GitHub's signing-key endpoint. Pull-request events, including
`pull_request_target`, are rejected even if other claims match. Environment and
reusable-workflow restrictions can be added as exact claim conditions.

Other OIDC issuers can be configured with HTTPS issuer/JWKS URLs, an exact
audience and subject, additional string-valued claim conditions, and a nonempty
app allowlist. GitLab publishes signing keys at `/oauth/discovery/keys`; bind
the policy to the immutable project identity and protected deployment context.
See the official [GitHub claim reference](https://docs.github.com/en/actions/reference/security/oidc)
and [GitLab ID-token documentation](https://docs.gitlab.com/ci/secrets/id_token_authentication/)
when choosing conditions. Browser-login OIDC configuration is independent.

## Run a CI command

With ShinyHub installed in a GitHub Actions job, grant the job `id-token: write`
and run:

```sh
shinyhub ci --host https://hub.example.com \
  --policy production --audience https://hub.example.com -- \
  deploy . --slug sales-dashboard --wait
```

The client requests the GitHub identity automatically using the runner's OIDC
request environment. `--audience` must match the configured policy exactly.
The request endpoint must be an HTTPS subdomain of `actions.githubusercontent.com`.
For other providers, supply a freshly issued token through a file or stdin:

```sh
shinyhub ci --host https://hub.example.com --policy gitlab-production \
  --identity-token-file /run/secrets/shinyhub-identity -- \
  deploy . --slug sales-dashboard --wait
```

For GitLab, declare an `id_tokens` entry with the policy's audience, then use
the resulting identity as the input file's contents. Keep that file private;
ShinyHub reads it but does not delete it. `--identity-token-file -` reads stdin.
Avoid printing identities or putting them in command arguments.

`--host` (or `SHINYHUB_HOST`) must be an explicit HTTPS URL. Saved host aliases
are not accepted. Put the child command and its flags after `--`; a second
`--host` there is rejected. Supported commands are `deploy`, `plan`, `apply`,
`doctor`, `fleet`, `apps`, `env`, `data`, `schedule`, and `whoami`.
The child receives the credential in its environment, and its output and exit
code pass through unchanged. Global output options before `--` are forwarded;
child options take precedence. No credential is written to the saved login
store, and GitHub's OIDC request credentials are removed from the child environment.

Each invocation obtains a new ten-minute credential. There is no renewal or
automatic exchange retry: commands making API requests after expiry fail and
need a new invocation with a fresh identity. Neither identity acquisition nor
the exchange follows redirects.

## Exchange contract

Send one JSON object to `POST /api/auth/trusted-publishing`:

```json
{"policy": "production", "identity_token": "<signed CI assertion>"}
```

On success the server returns HTTP 201 with `token`, `token_type: "Token"`,
`expires_at`, `expires_in: 600`, `apps`, and the credential `id`. Subsequent CLI
or API operations use the existing Token authentication scheme. The credential
has the developer role, is restricted to the listed apps, and cannot mint
persistent credentials or manage people. It may create a listed app that does
not exist yet. Expiry remains enforced during later API requests, so an
integration must account for operations lasting longer than ten minutes.

Clients must use an explicitly selected HTTPS destination, avoid redirects,
keep the assertion and returned token out of logs and command arguments, and
avoid persisting the short-lived token. Exchange responses use `no-store`.

The server verifies asymmetric signatures (RS256 or ES256), issuer, audience,
subject, expiry, issued-at time, optional not-before time, and every configured
claim. Assertions need a nonempty `jti` and may be at most ten minutes old.
An issuer/jti pair can be exchanged only once across all policies and HA
instances. The replay marker survives credential deletion; an uncertain or
failed exchange requires a fresh assertion before retrying.

## Revocation and operations

Removing or changing a policy invalidates its issued credentials once the new
configuration is loaded. Update every instance in an HA deployment; an instance
still running old configuration still enforces that configuration. Human
administrators can also revoke individual credentials through the existing
deployment service-account credential API or UI. Token expiry does not require
a restart. Exchange events and subsequent API actions retain audit attribution.

The exchange requires signing-key access and a writable database. Replay-store
errors fail closed. Signing keys are cached by the OIDC verifier. Database
migration 078 adds a small assertion-consumption table; expired replay markers
are pruned during later exchanges after a one-hour retention interval.
