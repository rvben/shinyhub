# Mint ShinyHub identity tokens so an app's own tests can exercise its
# identity-gated code.
#
# This exists because verification is strict by design. current_user() returns
# NULL only for a request that carried no token at all; anything present but
# unverifiable raises a shinyhub_identity_error. That is the right behaviour in
# production - a broken deployment must never render as an anonymous visitor -
# but it means an app author cannot test a signed-in code path by handing over a
# made-up string. They need a genuinely valid token, which means matching the
# issuer, audience, expiry and signature ShinyHub itself produces.
#
# The tokens are real, not stubs. They carry the same claim set the proxy mints,
# including which claims are OMITTED when empty, so an app that reads
# user$claims$email fails in a test exactly where it would fail in production.
# TestConformance_TestHelperMatchesProduction in internal/identity proves that
# field by field against the production Go minter, in both languages, and fails
# if either drifts.
#
# Names are prefixed here where the Python helper uses a testing submodule: R
# exports into the attached namespace, so shinyhub_test_token() cannot collide
# with an app's own fake_session(). What is symmetric across the two SDKs is the
# behaviour and the reason vocabulary, not the spelling. The Python counterparts
# are shinyhub_identity.testing.mint_token, .fake_session and .identity_env.
#
# None of this belongs in production code. The key is a fixed, published
# constant, so a token minted here is forgeable by anyone who has read this
# file; that is fine for a test and disqualifying anywhere else.

#' Default key for test tokens.
#'
#' A fixed 32-byte key, matching the length of ShinyHub's HKDF-derived per-app
#' key, so a token minted in one test verifies in another with no shared
#' fixture. Deliberately not random: a test that fails only on some runs because
#' two helpers disagreed about the key is worse than a forgeable test token, and
#' this key protects nothing.
#'
#' @return A raw vector of 32 bytes.
#' @examples
#' length(shinyhub_test_key())
#' @export
shinyhub_test_key <- function() {
  as.raw(0:31)
}

#' Default app slug for test tokens.
#'
#' @return The slug test tokens are minted for, as a string.
#' @examples
#' shinyhub_test_slug()
#' @export
shinyhub_test_slug <- function() {
  "test-app"
}

#' Lifetime of a test token, in seconds.
#'
#' Mirrors the server's own token TTL. A test token is not longer-lived than a
#' real one, so an app that mishandles a nearly-expired token can reproduce that
#' here.
#'
#' @return The number of seconds between \code{iat} and \code{exp}.
#' @examples
#' shinyhub_test_token_ttl()
#' @export
shinyhub_test_token_ttl <- function() {
  300
}

.shinyhub_test_resolve <- function(key, slug) {
  if (is.null(key)) {
    key <- shinyhub_test_key()
  }
  if (is.character(key)) {
    key <- sodium::hex2bin(key)
  }
  list(key = key, slug = if (is.null(slug)) shinyhub_test_slug() else slug)
}

#' Mint a valid identity token, shaped exactly as ShinyHub mints one.
#'
#' The defaults produce a token that \code{\link{current_user}} accepts with no
#' other setup, given \code{\link{with_shinyhub_identity}} or an explicit
#' \code{key}/\code{slug}.
#'
#' Every rejection path is reachable by changing one argument rather than by a
#' separate "make it broken" API, so a test names the thing that is wrong:
#'
#' \itemize{
#'   \item \code{expires_in = -60} gives reason \code{"expired"}
#'   \item another \code{key} gives \code{"bad_signature"}
#'   \item \code{slug = "other-app"} gives \code{"wrong_audience"} when verified
#'     as this app
#'   \item \code{issuer = "evil"} gives \code{"wrong_issuer"}
#' }
#'
#' A \code{"malformed"} token needs no helper: pass any non-JWT string as the
#' token.
#'
#' @param user_id Stamped as \code{sub} in string form, matching the server,
#'   which formats the numeric user ID.
#' @param username Stamped as \code{preferred_username}.
#' @param role The platform role.
#' @param app_role,email,name Omitted from the token entirely when empty, as the
#'   server omits them, so an app reading a claim the IdP never asserted fails
#'   here exactly as it would in production.
#' @param groups A character vector or list. Sorted before minting, because the
#'   server sorts before minting and a test asserting on order should see the
#'   real one. An empty value mints \code{null}, not \code{[]}, again matching
#'   the server.
#' @param groups_truncated Set when the user's group list was cut to the
#'   server's cap; omitted when \code{FALSE}.
#' @param key Raw key bytes or a hex string. Defaults to
#'   \code{\link{shinyhub_test_key}}.
#' @param slug The audience to mint for. Defaults to
#'   \code{\link{shinyhub_test_slug}}.
#' @param issuer The \code{iss} claim.
#' @param expires_in Seconds from \code{iat} to \code{exp}. Negative values mint
#'   an already-expired token.
#' @param issued_at The \code{iat} claim, in epoch seconds. Defaults to now.
#' @return The token, as a string.
#' @examples
#' token <- shinyhub_test_token(username = "alice", groups = c("admins"))
#' user <- verify_token(
#'   token,
#'   key = shinyhub_test_key(), slug = shinyhub_test_slug()
#' )
#' user$username
#' user$groups
#'
#' # One argument per rejection path.
#' tryCatch(
#'   verify_token(
#'     shinyhub_test_token(expires_in = -60),
#'     key = shinyhub_test_key(), slug = shinyhub_test_slug()
#'   ),
#'   shinyhub_identity_error = function(e) e$reason
#' )
#' @export
shinyhub_test_token <- function(user_id = 42,
                                username = "testuser",
                                role = "viewer",
                                app_role = "",
                                email = "",
                                name = "",
                                groups = character(0),
                                groups_truncated = FALSE,
                                key = NULL,
                                slug = NULL,
                                issuer = "shinyhub",
                                expires_in = shinyhub_test_token_ttl(),
                                issued_at = NULL) {
  resolved <- .shinyhub_test_resolve(key, slug)
  now <- if (is.null(issued_at)) floor(as.numeric(Sys.time())) else floor(as.numeric(issued_at))

  groups <- as.character(unlist(groups))
  groups <- groups[nzchar(groups)]

  # Claim presence follows the server's struct tags: role, groups and
  # preferred_username are always minted; app_role, email, name and
  # groups_truncated vanish when empty.
  #
  # Two encodings here are load-bearing and not the obvious choice:
  #   - `aud` must be a list, so it serializes as ["slug"] rather than "slug".
  #     jose::jwt_claim() rejects a list audience outright, which is why the
  #     claims are assembled and classed directly.
  #   - empty `groups` must be NA, which serializes as null. NULL would drop to
  #     {} and character(0) to [], and the server sends null.
  claims <- list(
    role = role,
    groups = if (length(groups) == 0) NA else as.list(sort(groups)),
    preferred_username = username,
    iss = issuer,
    sub = as.character(user_id),
    aud = list(resolved$slug),
    iat = now,
    exp = now + expires_in
  )
  if (nzchar(app_role)) claims$app_role <- app_role
  if (nzchar(email)) claims$email <- email
  if (nzchar(name)) claims$name <- name
  if (isTRUE(groups_truncated)) claims$groups_truncated <- TRUE

  jose::jwt_encode_hmac(
    structure(claims, class = c("jwt_claim", "list")),
    secret = resolved$key
  )
}

.shinyhub_test_token_arg <- function(token, ...) {
  if (is.null(token)) {
    return(shinyhub_test_token(...))
  }
  if (length(list(...)) > 0) {
    stop(
      "pass either token= or shinyhub_test_token() arguments, not both",
      call. = FALSE
    )
  }
  token
}

#' A session stand-in for \code{\link{current_user}}.
#'
#' Shaped like a real Shiny session: an environment carrying \code{request} (the
#' frozen handshake) and a \code{userData} environment. The \code{userData} part
#' matters - it is where the verified identity is cached for the session's
#' lifetime, so a stub without it would verify afresh on every call and quietly
#' not test the behaviour a real session has.
#'
#' @param token The token to carry. \code{NULL} (the default) mints one from
#'   \code{...}; \code{""} makes the session anonymous.
#' @param ... Passed to \code{\link{shinyhub_test_token}}.
#' @return An environment usable as the \code{session} argument to
#'   \code{\link{current_user}}.
#' @examples
#' with_shinyhub_identity({
#'   user <- current_user(shinyhub_test_session(username = "alice"))
#'   user$username
#' })
#'
#' # An anonymous visitor.
#' with_shinyhub_identity(
#'   is.null(current_user(shinyhub_test_session(token = "")))
#' )
#' @export
shinyhub_test_session <- function(token = NULL, ...) {
  session <- new.env(parent = emptyenv())
  session$request <- if (identical(token, "")) {
    list()
  } else {
    list(HTTP_X_SHINYHUB_IDENTITY_TOKEN = .shinyhub_test_token_arg(token, ...))
  }
  session$userData <- new.env(parent = emptyenv())
  session
}

#' Evaluate code with the environment ShinyHub injects.
#'
#' An app calls \code{current_user(session)} without passing a key, reading
#' \code{SHINYHUB_IDENTITY_KEY} and \code{SHINYHUB_APP_SLUG} from its
#' environment. To test that call as the app actually writes it, those variables
#' have to be set; this sets them to the same defaults the minting helpers use,
#' and restores the previous values afterwards, including their absence.
#'
#' @param code Code to evaluate. Evaluated once, with the variables set.
#' @param key,slug Defaults to \code{\link{shinyhub_test_key}} and
#'   \code{\link{shinyhub_test_slug}}.
#' @return The value of \code{code}.
#' @examples
#' with_shinyhub_identity(Sys.getenv("SHINYHUB_APP_SLUG"))
#' @export
with_shinyhub_identity <- function(code, key = NULL, slug = NULL) {
  resolved <- .shinyhub_test_resolve(key, slug)
  names <- c("SHINYHUB_IDENTITY_KEY", "SHINYHUB_APP_SLUG")
  previous <- vapply(
    names,
    function(n) Sys.getenv(n, unset = NA_character_),
    character(1)
  )
  on.exit({
    for (n in names) {
      if (is.na(previous[[n]])) {
        Sys.unsetenv(n)
      } else {
        do.call(Sys.setenv, as.list(previous[n]))
      }
    }
  })
  Sys.setenv(
    SHINYHUB_IDENTITY_KEY = sodium::bin2hex(resolved$key),
    SHINYHUB_APP_SLUG = resolved$slug
  )
  force(code)
}
