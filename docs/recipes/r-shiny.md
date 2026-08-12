# Deploy an R Shiny app

An R bundle needs two files: `app.R` at the root, and a `renv.lock` naming the
packages it depends on. Nothing else is required - no manifest, no launch
command, no renv project scaffolding.

```
my-r-app/
  app.R
  renv.lock
```

```bash
shinyhub connect https://hub.example.com
shinyhub deploy . --slug hello-r
```

## What the platform does with the bundle

1. **Detection.** A bundle with `app.R` at the root is an R app.
2. **Restore.** If `renv.lock` is present, the deploy runs `renv::restore()`
   inside the build sandbox, so the exact package versions the lockfile pins
   are installed before the app is ever started.
3. **Launch.** The app starts as
   `Rscript --no-save --no-restore --no-site-file --no-environ -e "shiny::runApp('.', host=…, port=…)"`,
   with the restored library on the search path.

The launch flags are `--vanilla` minus `--no-init-file`: the workspace and
host-environment isolation is kept, but the bundle's own `.Rprofile` is sourced,
which is what makes a full renv project work (see below).

## Two bundle layouts

Both are supported, and the difference is only in who owns the project library.

**Bare lockfile** (`app.R` + `renv.lock`, no `.Rprofile`, no `renv/`). ShinyHub
creates a project library at `.shinyhub-rlib/` inside the bundle, restores into
it, and prepends it to `.libPaths()` at launch. You do not need to know the
directory exists; it is bundle-relative, so it resolves against whatever path
the app's working directory has in the runtime that started it.

**Full renv project** (what `renv::init()` produces: `.Rprofile` sourcing
`renv/activate.R`, plus `renv/`). renv's own activation selects the project
library under `renv/library/…`, and ShinyHub does not interpose at all - the
bundle behaves exactly as it does when you run it locally. A hand-written
`.Rprofile` calling `renv::activate()` counts as the same layout.

Either way, generate the lockfile from a working local project:

```r
renv::init()      # first time: scans the code, installs, writes renv.lock
renv::snapshot()  # afterwards: refresh the lockfile from the project library
```

If you want the simplest possible bundle, keep `renv.lock` and drop the rest
before deploying. `.Rprofile` and `renv/` are also safe to ship; they are what
`renv::init()` wrote, and ShinyHub honours them.

## Bundles with their own launch command

A bundle that declares `[app] command` in `shinyhub.toml` (a plumber API, a
custom `Rscript` entrypoint) skips detection entirely, and with it the
deploy-time `renv::restore()` - the command owns its environment, so it also
owns getting its packages installed. The renv sandbox is still disabled for it,
because that is about being able to delete the app, not about how it launches.

## The renv sandbox is disabled

Builds and app processes run with `RENV_CONFIG_SANDBOX_ENABLED=FALSE`.

renv's sandbox is a copy of the system library made read-only (mode `0555`) to
stop a project from mutating packages it does not own. On ShinyHub that
protection is redundant - the build already runs confined, with the system
library read-only to it - and the read-only tree is actively harmful: nothing
in the app's own directory can unlink it, so a bundle that built one could not
be deleted afterwards.

This is a platform setting, not a default: setting
`RENV_CONFIG_SANDBOX_ENABLED` with `shinyhub env set` does not re-enable the
sandbox, because the launch-coupled environment wins over per-app variables on
a duplicate key. Deleting an app is not something a bundle gets to opt out of.

## Host requirements

The host running the app processes needs `R` and `Rscript` on `PATH`, with
`renv` installed in a library the service user can read:

```bash
Rscript -e 'install.packages("renv", repos = "https://cloud.r-project.org")'
```

Package installs are considerably faster when they resolve to binaries instead
of source. Point the builds at a binary-serving CRAN mirror in the service
environment:

```ini
Environment="RENV_CONFIG_REPOS_OVERRIDE=https://packagemanager.posit.co/cran/__linux__/noble/latest"
```

`RENV_CONFIG_REPOS_OVERRIDE` also reaches a private CRAN; see
[Environment variables](../environment.md#private-package-indexes) for the full
list of build variables that pass through.

## Persistent data and identity

Both work the same as for Python apps. Read and write files under `data/` to
survive redeploys:

```r
dir.create("data", showWarnings = FALSE)
write.csv(results, file.path("data", "results.csv"))
```

The proxy injects `X-Shinyhub-User`, `X-Shinyhub-Role` and the signed
`X-Shinyhub-Identity-Token` on every authenticated request; read them from the
Shiny session:

```r
server <- function(input, output, session) {
  user <- session$request$HTTP_X_SHINYHUB_USER
}
```

See [Identity forwarding](../identity.md) for the JWT verification reference.

## Troubleshooting

**`there is no package called '<name>'` at startup.** The package is not in
`renv.lock`. Re-run `renv::snapshot()` locally and redeploy; the restore only
installs what the lockfile pins.

**The build fails on a package that needs system libraries.** R source packages
link against system development headers (e.g. `libcurl4-openssl-dev` for
`curl`, `libxml2-dev` for `xml2`). Install them on the host, or point
`RENV_CONFIG_REPOS_OVERRIDE` at a mirror that serves binaries for your
platform, which avoids compiling altogether.

**Dependencies listed only in a `DESCRIPTION` file are not installed.**
ShinyHub restores from `renv.lock` and nothing else. A `DESCRIPTION`-only
bundle deploys and then fails at its first `library()` call.
