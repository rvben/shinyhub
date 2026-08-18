# shinyhub

Python distribution of the [ShinyHub](https://github.com/rvben/shinyhub) CLI.

ShinyHub is a self-hosted platform for deploying and managing Shiny apps
(Python or R). This package bundles the `shinyhub` Go binary so Python
users can install it with `pip` or `uv`:

```bash
uvx shinyhub serve
# or install it once:
uv tool install shinyhub
# or:
pip install shinyhub
```

## Usage

```bash
shinyhub --help                       # list subcommands
shinyhub serve                        # run the real local server
shinyhub connect https://...          # pair through browser or SSO
shinyhub deploy ./my-app --slug demo  # deploy an app
```

See [the project README](https://github.com/rvben/shinyhub) for full
documentation, server configuration, and Docker usage.

## Supported platforms

Prebuilt binaries ship for:

- Linux (amd64, arm64)
- macOS (amd64, arm64)

For other platforms, use the Docker image
(`ghcr.io/rvben/shinyhub:latest`) or
[build from source](https://github.com/rvben/shinyhub#from-source).
