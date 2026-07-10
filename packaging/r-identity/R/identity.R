# Read the signed identity ShinyHub forwards to a proxied app.
#
# ShinyHub injects a short-lived, per-app HS256 JWT
# (X-Shinyhub-Identity-Token) into every request it proxies, and hands the app
# its verification key via the SHINYHUB_IDENTITY_KEY (hex) and SHINYHUB_APP_SLUG
# environment variables. These helpers verify that token; the plain
# X-Shinyhub-* convenience headers must NOT be trusted for access decisions.

# Rejection reasons already warned about, so a misconfigured deployment warns
# once per distinct problem per session instead of once per request.
.shinyhub_warned <- new.env(parent = emptyenv())

# Warn that a PRESENT token was rejected. A genuine anonymous visitor sends no
# token at all, so a rejected token almost always means a misconfiguration
# (wrong or missing key, audience/issuer mismatch, clock skew) - high-signal,
# and safe to surface without leaking anything.
.shinyhub_warn_once <- function(reason_key, detail) {
  if (exists(reason_key, envir = .shinyhub_warned, inherits = FALSE)) {
    return(invisible(NULL))
  }
  assign(reason_key, TRUE, envir = .shinyhub_warned)
  warning(
    sprintf(
      paste0(
        "identity token present but rejected: %s ",
        "(the request is treated as anonymous; a rejected-but-present ",
        "token usually means the deployment is misconfigured)"
      ),
      detail
    ),
    call. = FALSE
  )
  invisible(NULL)
}

.shinyhub_reset_warned <- function() {
  rm(list = ls(envir = .shinyhub_warned), envir = .shinyhub_warned)
  invisible(NULL)
}

#' Verify a ShinyHub identity token.
#'
#' @param token The raw JWT string, or NULL/"" for an anonymous request.
#' @param key Raw key bytes or a hex string. Defaults to the
#'   \code{SHINYHUB_IDENTITY_KEY} environment variable (hex).
#' @param slug The expected audience (the app slug). Defaults to the
#'   \code{SHINYHUB_APP_SLUG} environment variable.
#' @return A named list of verified JWT claims (\code{preferred_username},
#'   \code{role}, \code{groups}, \code{sub}, ...), or \code{NULL} when the
#'   request is anonymous or the token fails any check.
#'
#'   Fail-closed diagnostics: a token that is \emph{present} but fails any
#'   check still returns \code{NULL}, and additionally raises a warning (once
#'   per distinct reason per session), because a present-but-rejected token
#'   usually means a misconfigured deployment rather than an anonymous
#'   visitor. A request without a token stays silent.
#' @export
verify_token <- function(token, key = NULL, slug = NULL) {
  if (is.null(token) || length(token) == 0 || !nzchar(token)) {
    return(NULL)
  }
  resolved <- .shinyhub_resolve_key(key)
  if (is.null(resolved$key)) {
    .shinyhub_warn_once("no_key", resolved$problem)
    return(NULL)
  }
  slug <- .shinyhub_resolve_slug(slug)
  if (is.null(slug)) {
    .shinyhub_warn_once(
      "no_slug",
      "expected audience unknown (SHINYHUB_APP_SLUG is unset or empty)"
    )
    return(NULL)
  }
  # jose validates the signature and exp (with its own grace); errors on any
  # failure. Treat any error as unauthenticated.
  decode_error <- NULL
  claims <- tryCatch(
    jose::jwt_decode_hmac(token, secret = resolved$key),
    error = function(e) {
      decode_error <<- conditionMessage(e)
      NULL
    }
  )
  if (is.null(claims)) {
    .shinyhub_warn_once(
      "decode",
      sprintf("token failed verification: %s", decode_error)
    )
    return(NULL)
  }
  # jose validates exp only when present; require it so a token that omits exp
  # cannot bypass the short-lived-token / replay bound.
  if (is.null(claims$exp)) {
    .shinyhub_warn_once("no_exp", "token carries no exp claim")
    return(NULL)
  }
  # jose does not check iss/aud; assert them ourselves.
  if (!identical(claims$iss, "shinyhub")) {
    .shinyhub_warn_once("bad_iss", "token issuer is not \"shinyhub\"")
    return(NULL)
  }
  if (!(slug %in% claims$aud)) {
    .shinyhub_warn_once(
      "bad_aud",
      sprintf("token audience does not include this app's slug (%s)", slug)
    )
    return(NULL)
  }
  claims
}

#' Verified identity of the current Shiny session, or NULL when anonymous.
#'
#' Call inside your Shiny \code{server} function.
#'
#' For local development (no ShinyHub proxy, so no token and no injected key),
#' setting \code{SHINYHUB_IDENTITY_DEV_USER} (and optionally
#' \code{SHINYHUB_IDENTITY_DEV_GROUPS} (comma-separated),
#' \code{SHINYHUB_IDENTITY_DEV_EMAIL}, \code{SHINYHUB_IDENTITY_DEV_NAME},
#' \code{SHINYHUB_IDENTITY_DEV_ROLE} (default \code{"viewer"})) makes this
#' return a synthetic claims list marked with \code{dev = TRUE}. It never
#' activates when \code{SHINYHUB_IDENTITY_KEY} is set - ShinyHub always
#' injects that key into app processes - so it cannot mask a real
#' verification failure in a deployment.
#'
#' @param session A Shiny session object; its \code{request} carries the
#'   forwarded header as \code{HTTP_X_SHINYHUB_IDENTITY_TOKEN}.
#' @param key,slug See \code{\link{verify_token}}.
#' @return Verified claims list, or \code{NULL} for an anonymous visitor.
#' @export
current_user <- function(session, key = NULL, slug = NULL) {
  token <- session$request$HTTP_X_SHINYHUB_IDENTITY_TOKEN
  no_token <- is.null(token) || length(token) == 0 || !nzchar(token)
  if (no_token && is.null(key)) {
    dev <- .shinyhub_dev_identity()
    if (!is.null(dev)) {
      return(dev)
    }
  }
  verify_token(token, key = key, slug = slug)
}

# Synthetic identity for local development, from SHINYHUB_IDENTITY_DEV_*.
# Only active when SHINYHUB_IDENTITY_KEY is absent: ShinyHub always injects
# that key into app processes, so under a real deployment this can never
# substitute for a missing or failed verification.
.shinyhub_dev_identity <- function() {
  username <- Sys.getenv("SHINYHUB_IDENTITY_DEV_USER", unset = "")
  if (!nzchar(username)) {
    return(NULL)
  }
  if (nzchar(Sys.getenv("SHINYHUB_IDENTITY_KEY", unset = ""))) {
    return(NULL)
  }
  groups <- trimws(strsplit(
    Sys.getenv("SHINYHUB_IDENTITY_DEV_GROUPS", unset = ""),
    ",",
    fixed = TRUE
  )[[1]])
  groups <- groups[nzchar(groups)]
  role <- Sys.getenv("SHINYHUB_IDENTITY_DEV_ROLE", unset = "viewer")
  if (!nzchar(role)) {
    role <- "viewer"
  }
  list(
    dev = TRUE,
    sub = username,
    preferred_username = username,
    role = role,
    email = Sys.getenv("SHINYHUB_IDENTITY_DEV_EMAIL", unset = ""),
    name = Sys.getenv("SHINYHUB_IDENTITY_DEV_NAME", unset = ""),
    groups = as.list(groups)
  )
}

# Resolve the verification key, returning list(key =, problem =). Exactly one
# of the two is non-NULL: a resolved key carries no problem, and a problem
# string describes why no key is available.
.shinyhub_resolve_key <- function(key) {
  if (is.null(key)) {
    key <- Sys.getenv("SHINYHUB_IDENTITY_KEY", unset = "")
    if (!nzchar(key)) {
      return(list(
        key = NULL,
        problem = "no verification key (SHINYHUB_IDENTITY_KEY is unset or empty)"
      ))
    }
  }
  if (is.character(key)) {
    # sodium::hex2bin does not error on garbage - it skips invalid characters
    # and can return raw(0) - so an empty parse of a non-empty string is the
    # not-hex signal.
    parsed <- tryCatch(sodium::hex2bin(key), error = function(e) NULL)
    if (is.null(parsed) || length(parsed) == 0) {
      return(list(
        key = NULL,
        problem = "verification key is not valid hex (check SHINYHUB_IDENTITY_KEY)"
      ))
    }
    return(list(key = parsed, problem = NULL))
  }
  list(key = key, problem = NULL) # already raw bytes
}

.shinyhub_resolve_slug <- function(slug) {
  if (is.null(slug)) {
    slug <- Sys.getenv("SHINYHUB_APP_SLUG", unset = "")
  }
  if (!nzchar(slug)) {
    return(NULL)
  }
  slug
}
