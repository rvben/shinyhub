---
description: "Deploy an R Shiny app from two files, app.R at the bundle root and an renv.lock naming its packages. No manifest is required."
---

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

You do not have to clean the restored library out first. `renv/library/` is on
the platform's always-skipped list, so deploying a working project directory
uploads the lockfile and the activation scripts and leaves the library behind.
That matters because `renv::install()` fills `renv/library/` with symlinks into
a machine-local renv cache: those links resolve on your machine and nowhere
else, and the server restores the library from `renv.lock` anyway. The exclusion
is platform policy, not an ignore rule, so it applies even though `renv::init()`
writes its own ignore file inside `renv/` rather than at the bundle root (only a
root-level `.gitignore` or `.shinyhubignore` is read; see
[Bundle filtering](bundle-filtering.md)).

## Two-file apps (`ui.R` + `server.R`)

The classic two-file layout deploys as it is. Detection accepts an R bundle in
either form: `app.R` at the bundle root, or `ui.R` and `server.R` beside each
other with no `app.R`. Both launch through `shiny::runApp('.')`, which reads
whichever layout it finds, so both get the deploy-time `renv::restore()` and
the interposed project library at launch. There is no `app.R` shim to write and
no `[app] command` to declare.

`server.R` is the file detection keys on, because a `ui.R` on its own cannot be
run. A bundle holding only `ui.R` is still reported as having no entrypoint:

```
no app entrypoint found in <dir> (add app.py, app.R, or ui.R and server.R, or declare [app] command in shinyhub.toml)
```

If a bundle contains both `app.R` and `server.R`, `app.R` wins, which is the
same precedence `shiny::runApp()` applies.

An `app.py` anywhere in the bundle root still takes precedence over either R
layout, and the deploy says so rather than silently running the wrong runtime:

```
Warning: both app.py and server.R found; app.py takes precedence and this
deploys as a Python app. Remove app.py, or declare `[app] command` in
shinyhub.toml, to deploy the R app.
```

## Bundles with their own launch command

A bundle that declares `[app] command` in `shinyhub.toml` (a plumber API, a
custom `Rscript` entrypoint) skips detection entirely, and with it the
deploy-time `renv::restore()` - the command owns its environment, so it also
owns getting its packages installed. The renv sandbox is still disabled for it,
because that is about being able to delete the app, not about how it launches.

The failure mode when you miss this is a startup error rather than a build
error: dependencies report ready, the replica then fails its health check, and
the log lines printed with the failure end at
`Error in loadNamespace(x) : there is no package called 'shiny'`. Either ship a
full renv project whose `.Rprofile` restores on startup, or inline the restore
into the command:

```toml
# shinyhub.toml
[app]
command = ["Rscript", "--no-save", "--no-restore", "-e",
           "renv::restore(prompt = FALSE); shiny::runApp('.', host = '{host}', port = {port})"]
```

`command` is an array of argv tokens, not a shell string; a string value is
rejected at parse time with `incompatible types: TOML value has type string;
destination has type slice`. `{host}` and `{port}` are substituted with the
address the platform assigned to that replica. See
[Manifest](../manifest.md#app-command) for the full field reference.

Inlining the restore this way costs two things the detected launch path gives
you for free:

- `renv::restore()` runs on every replica start, not once at deploy time.
- Without `.Rprofile` and `renv/activate.R` in the bundle, renv is not
  activated, so the restore installs into the first entry of `.libPaths()`,
  which is a library shared with the host rather than a per-app one. The
  interposed `.shinyhub-rlib/` library described above is set up by the
  detected-launch path, and a bundle-declared command does not get it.

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

**The deploy spends about two minutes on readiness and then reports that the
app "likely crashed on startup", but the log says it is listening.** The bundle
calls `shiny::runApp()` itself. ShinyHub launches
`shiny::runApp('.', host=…, port=…)` with the address it assigned, and
`runApp('.')` sources `app.R` to obtain the app object; a `runApp()` call left
inside `app.R` therefore runs first and binds the host and port hardcoded there.
The platform keeps probing the port it assigned, which nothing is listening on,
and fails the replica when the health-check window expires. The tell is in the
log lines printed with the failure:

```
Loading required package: shiny
Listening on http://127.0.0.1:9922
```

That port is the app's own, not the one the platform assigned. If the hardcoded
port is already taken, the same bundle fails faster and more visibly instead,
with `createTcpServer: address already in use` and `Execution halted`.

The fix is to end `app.R` with the app object and let the platform start it:

```r
shinyApp(ui, server)   # not runApp(shinyApp(ui, server), port = 9922)
```

RStudio's Run App button works either way, so this is easy to leave in. If the
bundle genuinely has to own its own launch, declare `[app] command` and use the
`{host}` and `{port}` placeholders rather than fixed values.

**The build fails on a package that needs system libraries.** R source packages
link against system development headers (e.g. `libcurl4-openssl-dev` for
`curl`, `libxml2-dev` for `xml2`). Install them on the host, or point
`RENV_CONFIG_REPOS_OVERRIDE` at a mirror that serves binaries for your
platform, which avoids compiling altogether.

**Dependencies listed only in a `DESCRIPTION` file are not installed.**
ShinyHub restores from `renv.lock` and nothing else. A `DESCRIPTION`-only
bundle deploys and then fails at its first `library()` call.
