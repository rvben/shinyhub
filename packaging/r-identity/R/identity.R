# Read the signed identity ShinyHub forwards to a proxied app.
#
# ShinyHub injects a short-lived, per-app HS256 JWT
# (X-Shinyhub-Identity-Token) into every request it proxies, and hands the app
# its verification key via the SHINYHUB_IDENTITY_KEY (hex) and SHINYHUB_APP_SLUG
# environment variables. These helpers verify that token; the plain
# X-Shinyhub-* convenience headers must NOT be trusted for access decisions.
#
# NULL means exactly one thing: the request carried no identity token, so the
# visitor is anonymous. A token that IS present but fails verification raises a
# "shinyhub_identity_error" condition instead, because that is a broken
# deployment and rendering it as "anonymous" hides the outage behind an empty
# dashboard. The reason vocabulary is identical to the Python helper's.

# Raise a rejection. The condition carries `reason` (stable, for code to branch
# on) and `detail` (human-readable, for the operator's log line), so an app can
# do tryCatch(..., shinyhub_identity_error = function(e) e$reason).
.shinyhub_error <- function(reason, detail) {
  stop(structure(
    class = c("shinyhub_identity_error", "error", "condition"),
    list(
      message = sprintf("identity token rejected (%s): %s", reason, detail),
      call = NULL,
      reason = reason,
      detail = detail
    )
  ))
}

# jose reports every rejection as a plain error, so the reason is recovered from
# the message. The exact messages this classifies are pinned in
# tests/testthat/test-errors.R: a jose wording change fails there rather than
# silently collapsing every rejection into "malformed".
.shinyhub_decode_reason <- function(message) {
  if (grepl("has expired", message, fixed = TRUE)) {
    return("expired")
  }
  if (grepl("signature", message, ignore.case = TRUE)) {
    return("bad_signature")
  }
  "malformed"
}

.shinyhub_chr <- function(value, default = "") {
  if (is.null(value) || length(value) == 0) default else as.character(value)[[1]]
}

# Normalize verified claims into the identity shape, whose field names match the
# Python helper's Identity so one documented contract covers both SDKs. The raw
# claims stay available under $claims.
.shinyhub_identity <- function(claims) {
  groups <- claims$groups
  structure(
    list(
      user_id = .shinyhub_chr(claims$sub),
      username = .shinyhub_chr(claims$preferred_username),
      role = .shinyhub_chr(claims$role),
      groups = if (is.null(groups)) character(0) else as.character(unlist(groups)),
      name = .shinyhub_chr(claims$name),
      email = .shinyhub_chr(claims$email),
      groups_truncated = isTRUE(claims$groups_truncated),
      claims = claims
    ),
    class = "shinyhub_identity"
  )
}

#' Verify a ShinyHub identity token.
#'
#' @param token The raw JWT string.
#' @param key Raw key bytes or a hex string. Defaults to the
#'   \code{SHINYHUB_IDENTITY_KEY} environment variable (hex).
#' @param slug The expected audience (the app slug). Defaults to the
#'   \code{SHINYHUB_APP_SLUG} environment variable.
#' @param leeway Seconds of clock skew allowed past \code{exp}, matching the
#'   Python helper's default so both SDKs accept and reject the same token.
#'   Values above 60 have no effect: jose applies its own 60-second grace and
#'   has already rejected the token by then.
#' @return A \code{shinyhub_identity}: a list of \code{user_id},
#'   \code{username}, \code{role}, \code{groups}, \code{name}, \code{email},
#'   \code{groups_truncated}, and the raw \code{claims}.
#'
#'   Every failure raises a \code{shinyhub_identity_error} condition, including
#'   an absent token (reason \code{"no_token"}). This primitive is for code that
#'   already knows a token is expected; use \code{\link{current_user}} when an
#'   absent token means "anonymous". The condition's \code{reason} is one of
#'   \code{no_token}, \code{no_key}, \code{bad_key}, \code{no_slug},
#'   \code{bad_signature}, \code{expired}, \code{wrong_audience},
#'   \code{wrong_issuer}, \code{malformed}.
#' @export
verify_token <- function(token, key = NULL, slug = NULL, leeway = 30) {
  if (is.null(token) || length(token) == 0 || !nzchar(token)) {
    .shinyhub_error("no_token", "no identity token to verify")
  }
  key <- .shinyhub_resolve_key(key)
  slug <- .shinyhub_resolve_slug(slug)

  # ShinyHub mints HS256 only, and jose verifies whatever HMAC size the token's
  # own header asks for. Pin the algorithm so this helper rejects exactly what
  # the Python helper's algorithms=["HS256"] rejects.
  split_error <- NULL
  header <- tryCatch(
    jose::jwt_split(token)$header,
    error = function(e) {
      split_error <<- conditionMessage(e)
      NULL
    }
  )
  if (!is.null(split_error)) {
    .shinyhub_error(
      .shinyhub_decode_reason(split_error),
      sprintf("token could not be parsed: %s", split_error)
    )
  }
  if (!identical(header$alg, "HS256")) {
    .shinyhub_error("malformed", sprintf(
      "token algorithm is %s, expected HS256",
      if (is.null(header$alg)) "absent" else header$alg
    ))
  }

  decode_error <- NULL
  claims <- tryCatch(
    jose::jwt_decode_hmac(token, secret = key),
    error = function(e) {
      decode_error <<- conditionMessage(e)
      NULL
    }
  )
  if (is.null(claims)) {
    .shinyhub_error(
      .shinyhub_decode_reason(decode_error),
      sprintf("token failed verification: %s", decode_error)
    )
  }
  # jose validates exp only when present; require it so a token that omits exp
  # cannot bypass the short-lived-token / replay bound. A claim ShinyHub always
  # mints but this token lacks is malformed, not "wrong": the Python helper
  # classifies a missing exp/iss/aud the same way.
  if (is.null(claims$exp)) {
    .shinyhub_error("malformed", "token carries no exp claim")
  }
  # jose applies a fixed 60-second grace of its own, so enforce `leeway` here or
  # a token 45 seconds past exp would be accepted in R and rejected in Python.
  if (as.numeric(claims$exp) + leeway < as.numeric(Sys.time())) {
    .shinyhub_error("expired", sprintf(
      "token expired at %s",
      format(as.POSIXct(as.numeric(claims$exp), origin = "1970-01-01", tz = "UTC"),
        "%Y-%m-%dT%H:%M:%SZ",
        tz = "UTC"
      )
    ))
  }
  # jose does not check iss/aud; assert them ourselves.
  if (is.null(claims$iss)) {
    .shinyhub_error("malformed", "token carries no iss claim")
  }
  if (!identical(claims$iss, "shinyhub")) {
    .shinyhub_error("wrong_issuer", "token issuer is not \"shinyhub\"")
  }
  if (is.null(claims$aud)) {
    .shinyhub_error("malformed", "token carries no aud claim")
  }
  if (!(slug %in% claims$aud)) {
    .shinyhub_error(
      "wrong_audience",
      sprintf("token audience does not include this app's slug (%s)", slug)
    )
  }
  .shinyhub_identity(claims)
}

#' Verified identity of the current Shiny session, or NULL when anonymous.
#'
#' Call inside your Shiny \code{server} function.
#'
#' The session's handshake token is verified once, on the first call, and the
#' same answer is returned for the rest of the session's life. That is a
#' correctness property, not a cache: the handshake request is frozen for the
#' life of a Shiny session while the token it carries expires five minutes in,
#' so re-verifying it from a reactive would start failing part-way through a
#' long session even though nothing about the user changed. \code{key} and
#' \code{slug} are therefore read on the first call only.
#'
#' For local development (no ShinyHub proxy, so no token and no injected key),
#' setting \code{SHINYHUB_IDENTITY_DEV_USER} (and optionally
#' \code{SHINYHUB_IDENTITY_DEV_GROUPS} (comma-separated),
#' \code{SHINYHUB_IDENTITY_DEV_EMAIL}, \code{SHINYHUB_IDENTITY_DEV_NAME},
#' \code{SHINYHUB_IDENTITY_DEV_ROLE} (default \code{"viewer"})) makes this
#' return a synthetic identity whose claims are marked \code{dev = TRUE}. It
#' never activates when \code{SHINYHUB_IDENTITY_KEY} is set - ShinyHub always
#' injects that key into app processes - so it cannot mask a real verification
#' failure in a deployment.
#'
#' @param session A Shiny session object; its \code{request} carries the
#'   forwarded header as \code{HTTP_X_SHINYHUB_IDENTITY_TOKEN}.
#' @param key,slug,leeway See \code{\link{verify_token}}.
#' @return A \code{shinyhub_identity}, or \code{NULL} for an anonymous visitor.
#'   A token that is present but fails verification raises a
#'   \code{shinyhub_identity_error} condition on every call, with the same
#'   \code{reason}, rather than degrading into \code{NULL}.
#' @export
current_user <- function(session, key = NULL, slug = NULL, leeway = 30) {
  cache <- .shinyhub_cache(session)
  if (!is.null(cache) && exists("outcome", envir = cache, inherits = FALSE)) {
    return(.shinyhub_replay(get("outcome", envir = cache)))
  }
  outcome <- tryCatch(
    list(
      identity = .shinyhub_verify_session(session, key, slug, leeway),
      error = NULL
    ),
    shinyhub_identity_error = function(e) list(identity = NULL, error = e)
  )
  if (!is.null(cache)) {
    assign("outcome", outcome, envir = cache)
  }
  .shinyhub_replay(outcome)
}

.shinyhub_verify_session <- function(session, key, slug, leeway) {
  token <- session$request$HTTP_X_SHINYHUB_IDENTITY_TOKEN
  if (is.null(token) || length(token) == 0 || !nzchar(token)) {
    if (is.null(key)) {
      dev <- .shinyhub_dev_identity()
      if (!is.null(dev)) {
        return(dev)
      }
    }
    return(NULL)
  }
  verify_token(token, key = key, slug = slug, leeway = leeway)
}

.shinyhub_replay <- function(outcome) {
  if (!is.null(outcome$error)) {
    stop(outcome$error)
  }
  outcome$identity
}

# The session's own userData environment holds the verified outcome, so it is
# released with the session and cannot leak across sessions.
#
# A session-shaped list (an app's test double) has no userData environment and
# is verified on every call. That is right for a stub: the staleness this
# guards against needs a real, long-lived session whose frozen handshake token
# ages past its exp.
.shinyhub_cache <- function(session) {
  store <- session$userData
  if (!is.environment(store)) {
    return(NULL)
  }
  slot <- store$.shinyhub_identity
  if (!is.environment(slot)) {
    slot <- new.env(parent = emptyenv())
    assign(".shinyhub_identity", slot, envir = store)
  }
  slot
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
  .shinyhub_identity(list(
    dev = TRUE,
    sub = username,
    preferred_username = username,
    role = role,
    email = Sys.getenv("SHINYHUB_IDENTITY_DEV_EMAIL", unset = ""),
    name = Sys.getenv("SHINYHUB_IDENTITY_DEV_NAME", unset = ""),
    groups = as.list(groups)
  ))
}

# Resolve the verification key to raw bytes, raising when unavailable.
.shinyhub_resolve_key <- function(key) {
  if (is.null(key)) {
    key <- Sys.getenv("SHINYHUB_IDENTITY_KEY", unset = "")
    if (!nzchar(key)) {
      .shinyhub_error(
        "no_key",
        "no verification key (SHINYHUB_IDENTITY_KEY is unset or empty)"
      )
    }
  }
  if (is.character(key)) {
    # sodium::hex2bin does not error on garbage - it skips invalid characters
    # and can return raw(0) - so an empty parse of a non-empty string is the
    # not-hex signal.
    parsed <- tryCatch(sodium::hex2bin(key), error = function(e) NULL)
    if (is.null(parsed) || length(parsed) == 0) {
      .shinyhub_error(
        "bad_key",
        "verification key is not valid hex (check SHINYHUB_IDENTITY_KEY)"
      )
    }
    return(parsed)
  }
  key # already raw bytes
}

.shinyhub_resolve_slug <- function(slug) {
  if (is.null(slug)) {
    slug <- Sys.getenv("SHINYHUB_APP_SLUG", unset = "")
  }
  if (!nzchar(slug)) {
    .shinyhub_error(
      "no_slug",
      "expected audience unknown (SHINYHUB_APP_SLUG is unset or empty)"
    )
  }
  slug
}
