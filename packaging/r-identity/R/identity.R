# Read the signed identity ShinyHub forwards to a proxied app.
#
# ShinyHub injects a short-lived, per-app HS256 JWT
# (X-Shinyhub-Identity-Token) into every request it proxies, and hands the app
# its verification key via the SHINYHUB_IDENTITY_KEY (hex) and SHINYHUB_APP_SLUG
# environment variables. These helpers verify that token; the plain
# X-Shinyhub-* convenience headers must NOT be trusted for access decisions.

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
#' @export
verify_token <- function(token, key = NULL, slug = NULL) {
  if (is.null(token) || length(token) == 0 || !nzchar(token)) {
    return(NULL)
  }
  key <- .shinyhub_resolve_key(key)
  slug <- .shinyhub_resolve_slug(slug)
  if (is.null(key) || is.null(slug)) {
    return(NULL)
  }
  # jose validates the signature and exp (with its own grace); errors on any
  # failure. Treat any error as unauthenticated.
  claims <- tryCatch(
    jose::jwt_decode_hmac(token, secret = key),
    error = function(e) NULL
  )
  if (is.null(claims)) {
    return(NULL)
  }
  # jose validates exp only when present; require it so a token that omits exp
  # cannot bypass the short-lived-token / replay bound.
  if (is.null(claims$exp)) {
    return(NULL)
  }
  # jose does not check iss/aud; assert them ourselves.
  if (!identical(claims$iss, "shinyhub")) {
    return(NULL)
  }
  if (!(slug %in% claims$aud)) {
    return(NULL)
  }
  claims
}

#' Verified identity of the current Shiny session, or NULL when anonymous.
#'
#' Call inside your Shiny \code{server} function.
#'
#' @param session A Shiny session object; its \code{request} carries the
#'   forwarded header as \code{HTTP_X_SHINYHUB_IDENTITY_TOKEN}.
#' @param key,slug See \code{\link{verify_token}}.
#' @return Verified claims list, or \code{NULL} for an anonymous visitor.
#' @export
current_user <- function(session, key = NULL, slug = NULL) {
  token <- session$request$HTTP_X_SHINYHUB_IDENTITY_TOKEN
  verify_token(token, key = key, slug = slug)
}

.shinyhub_resolve_key <- function(key) {
  if (is.null(key)) {
    hexkey <- Sys.getenv("SHINYHUB_IDENTITY_KEY", unset = "")
    if (!nzchar(hexkey)) {
      return(NULL)
    }
    return(tryCatch(sodium::hex2bin(hexkey), error = function(e) NULL))
  }
  if (is.character(key)) {
    return(tryCatch(sodium::hex2bin(key), error = function(e) NULL))
  }
  key # already raw bytes
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
