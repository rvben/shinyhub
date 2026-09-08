cli_path <- function() {
  executable <- getOption("shinyhub.executable", "shinyhub")
  if (!is.character(executable) || length(executable) != 1L || !nzchar(executable)) {
    stop("Set options(shinyhub.executable = '/path/to/shinyhub').", call. = FALSE)
  }
  if (!file.exists(executable) && !nzchar(Sys.which(executable))) {
    stop("Install the CLI with 'uv tool install shinyhub', or set options(shinyhub.executable = '/path/to/shinyhub').", call. = FALSE)
  }
  executable
}

require_editor <- function() {
  if (!rstudioapi::isAvailable()) stop("This addin requires RStudio. Use the ShinyHub CLI outside RStudio.", call. = FALSE)
  # terminalExecute uses the selected terminal shell. POSIX quoting is safe
  # for Unix shells; do not pretend it also handles arbitrary Windows shells.
  if (.Platform$OS.type == "windows") stop("These addins currently support macOS and Linux. Use the ShinyHub CLI on Windows.", call. = FALSE)
}

app_dir <- function(start, root) {
  root <- normalizePath(root, mustWork = TRUE)
  dir <- normalizePath(if (dir.exists(start)) start else dirname(start), mustWork = TRUE)
  if (dir != root && !startsWith(dir, paste0(root, "/"))) return(root)
  repeat {
    if (any(file.exists(file.path(dir, c("shinyhub.toml", "app.R", "server.R", "app.py", "fleet.toml"))))) return(dir)
    if (dir == root) return(root)
    dir <- dirname(dir)
  }
}

project_dir <- function() {
  root <- rstudioapi::getActiveProject()
  if (is.null(root)) root <- getwd()
  active <- rstudioapi::getSourceEditorContext()$path
  if (!nzchar(active)) active <- root
  app_dir(active, root)
}

cli_output <- function(args) {
  output <- suppressWarnings(system2(cli_path(), shQuote(args), stdout = TRUE, stderr = TRUE))
  status <- attr(output, "status")
  list(output = output, status = if (is.null(status)) 0L else status)
}

choose_host <- function() {
  response <- cli_output(c("hosts", "--output=json"))
  if (response$status != 0L) stop("Could not list servers. Run shinyhub hosts in a terminal.", call. = FALSE)
  hosts <- jsonlite::fromJSON(paste(response$output, collapse = "\n"))$items
  if (is.null(hosts) || NROW(hosts) == 0L) stop("Use the 'Connect to ShinyHub' addin first.", call. = FALSE)
  labels <- paste(ifelse(nzchar(hosts$name), hosts$name, hosts$host), hosts$host, sep = " - ")
  selected <- utils::select.list(labels, title = "Publish to which ShinyHub server?", graphics = TRUE)
  if (!nzchar(selected)) return(NULL)
  hosts$host[match(selected, labels)]
}

terminal_command <- function(args, cleanup = NULL) {
  command <- paste(shQuote(c(cli_path(), args), type = "sh"), collapse = " ")
  if (is.null(cleanup)) return(command)
  remove_plan <- paste("rm -f --", shQuote(cleanup, type = "sh"))
  retry <- paste(shQuote(c(cli_path(), args, "--allow-downtime"), type = "sh"), collapse = " ")
  paste0("trap ", shQuote(remove_plan, type = "sh"), " EXIT; ", command,
    "; shinyhub_status=$?; if [ \"$shinyhub_status\" -eq 5 ]; then ",
    "printf '%s\\n' 'The working version was preserved. Retrying will stop the app and disconnect active sessions.' ",
    "'The same reviewed bundle will be deployed; later source edits are excluded.' ",
    "'Type deploy to approve downtime, or press Enter to cancel:'; ",
    "IFS= read -r shinyhub_answer; if [ \"$shinyhub_answer\" = deploy ]; then ",
    retry, "; shinyhub_status=$?; fi; fi; ",
    "if [ \"$shinyhub_status\" -eq 2 ]; then printf '%s\\n' 'Server state changed. Publish again to review a new plan.'; fi; ",
    "exit \"$shinyhub_status\"")
}

terminal_cli <- function(args, dir, cleanup = NULL) {
  command <- terminal_command(args, cleanup)
  id <- rstudioapi::terminalExecute(command, workingDir = dir, show = TRUE)
  if (is.null(id)) stop("RStudio could not start a terminal.", call. = FALSE)
  invisible(id)
}

check_app <- function() {
  require_editor()
  host <- choose_host()
  if (!is.null(host)) terminal_cli(c("doctor", project_dir(), paste0("--host=", host)), project_dir())
}

preview_deploy <- function() {
  require_editor()
  host <- choose_host()
  if (!is.null(host)) terminal_cli(c("plan", project_dir(), paste0("--host=", host)), project_dir())
}

develop_app <- function() {
  require_editor()
  terminal_cli(c("dev", project_dir(), "--open"), project_dir())
}

publish_app <- function() {
  require_editor()
  host <- choose_host()
  if (is.null(host)) return(invisible(NULL))
  dir <- project_dir()
  rstudioapi::documentSaveAll()
  plan <- tempfile("shinyhub-publish-", fileext = ".plan")
  launched <- FALSE
  on.exit(if (!launched) unlink(plan), add = TRUE)
  for (step in c("doctor", "plan")) {
    args <- c(step, dir, paste0("--host=", host), "--output=table")
    if (step == "plan") args <- c(args, "--out", plan)
    response <- cli_output(args)
    cat(paste(response$output, collapse = "\n"), "\n")
    if (response$status != 0L) stop("Preflight failed. Review the output in the Console before publishing.", call. = FALSE)
  }
  if (rstudioapi::showQuestion("Publish app", paste0("Deploy ", basename(dir), " to ", host, "? Review the deployment plan in the Console first."), "Deploy", "Cancel")) {
    terminal_cli(c("apply", plan, paste0("--host=", host)), dir, cleanup = plan)
    launched <- TRUE
  }
  invisible(NULL)
}

connect_server <- function() {
  require_editor()
  host <- rstudioapi::showPrompt("Connect to ShinyHub", "Server URL", "https://")
  if (is.null(host)) return(invisible(NULL))
  if (!grepl("^https?://[^/@?#[:space:]]+(/[^?#[:space:]]*)?$", host)) stop("Enter an HTTP(S) URL without credentials, query, or fragment.", call. = FALSE)
  terminal_cli(c("connect", host), project_dir())
}
