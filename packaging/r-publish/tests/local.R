library(shinyhub)
dir <- tempfile("shinyhub-r-addin-")
dir.create(dir)
tryCatch({
  nested <- file.path(dir, "apps", "sales ' $(echo nope)")
  dir.create(file.path(nested, "helpers"), recursive = TRUE)
  file.create(file.path(nested, "shinyhub.toml"))
  file.create(file.path(nested, "helpers", "helper.R"))
  stopifnot(identical(shinyhub:::app_dir(file.path(nested, "helpers", "helper.R"), dir), normalizePath(nested)))
  stopifnot(identical(shinyhub:::app_dir(tempdir(), dir), normalizePath(dir)))
  if (.Platform$OS.type != "windows") {
    executable <- file.path(dir, "fake cli ' name")
    writeLines(c("#!/bin/sh", "printf '%s\\n' \"$@\""), executable)
    Sys.chmod(executable, "0700")
    previous <- options(shinyhub.executable = executable)
    args <- c("space here", "quote's", "$(touch should-not-exist)", "; exit 19")
    result <- shinyhub:::cli_output(args)
    options(previous)
    stopifnot(result$status == 0L, identical(result$output, args))
  }
}, finally = unlink(dir, recursive = TRUE))

if (.Platform$OS.type != "windows") {
  work <- tempfile("shinyhub-retry-")
  dir.create(work)
  tryCatch({
    cli <- file.path(work, "fake cli")
    calls <- file.path(work, "calls")
    writeLines(c("#!/bin/sh", paste("printf '%s\\n' \"$@\" >>", shQuote(calls)),
      "for arg do if [ \"$arg\" = --allow-downtime ]; then exit 0; fi; done",
      "exit \"$SHINYHUB_TEST_EXIT\""), cli)
    Sys.chmod(cli, "0700")
    previous <- options(shinyhub.executable = cli)
    for (scenario in c("approve", "cancel", "stale")) {
      plan <- file.path(work, "plan ' $(echo unsafe).plan")
      writeLines("reviewed", plan)
      unlink(calls)
      Sys.setenv(SHINYHUB_TEST_EXIT = if (scenario == "stale") "2" else "5")
      command <- shinyhub:::terminal_command(c("apply", plan, "--host=https://hub.example"), cleanup = plan)
      script <- file.path(work, "publish.sh")
      writeLines(command, script)
      output <- suppressWarnings(system2("sh", shQuote(script), input = if (scenario == "approve") "deploy" else "", stdout = TRUE, stderr = TRUE))
      recorded <- readLines(calls)
      stopifnot(!file.exists(plan), sum(recorded == "apply") == if (scenario == "approve") 2L else 1L,
        sum(recorded == "--allow-downtime") == if (scenario == "approve") 1L else 0L,
        sum(recorded == plan) == if (scenario == "approve") 2L else 1L)
    }
    options(previous)
    Sys.unsetenv("SHINYHUB_TEST_EXIT")
  }, finally = unlink(work, recursive = TRUE))
}
