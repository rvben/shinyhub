# Shared test helpers, loaded by testthat before every test file.

# Run `code` with the given environment variables set (a value of NA unsets
# the variable), restoring the previous values afterwards.
with_env <- function(vars, code) {
  old <- vapply(
    names(vars),
    function(n) Sys.getenv(n, unset = NA_character_),
    character(1)
  )
  on.exit({
    for (n in names(old)) {
      if (is.na(old[[n]])) {
        Sys.unsetenv(n)
      } else {
        do.call(Sys.setenv, as.list(old[n]))
      }
    }
  })
  for (n in names(vars)) {
    v <- vars[[n]]
    if (is.na(v)) {
      Sys.unsetenv(n)
    } else {
      do.call(Sys.setenv, stats::setNames(list(v), n))
    }
  }
  force(code)
}

# 32-byte key, matching ShinyHub's HKDF-SHA256-derived per-app key length.
dkey <- sodium::hex2bin(paste0(
  "00112233445566778899aabbccddeeff",
  "00112233445566778899aabbccddeeff"
))
dwrong_key <- sodium::hex2bin(paste(rep("ff", 32), collapse = ""))
dslug <- "sales-dashboard"

dmint <- function(k = dkey, s = dslug, iss = "shinyhub", role = "admin",
                  groups = list("team-a", "team-b"), username = "alice",
                  email = "alice@example.com", name = "Alice Liddell",
                  sub = "42", exp = as.numeric(Sys.time()) + 300) {
  claims <- jose::jwt_claim(
    iss = iss, sub = sub, aud = s,
    preferred_username = username, role = role, email = email, name = name,
    groups = groups, exp = exp
  )
  jose::jwt_encode_hmac(claims, secret = k)
}

# Session stand-in shaped like a real Shiny session: an environment carrying
# `request` (the frozen handshake) and a `userData` environment.
fake_session <- function(token = NULL) {
  s <- new.env(parent = emptyenv())
  s$request <- if (is.null(token)) list() else list(HTTP_X_SHINYHUB_IDENTITY_TOKEN = token)
  s$userData <- new.env(parent = emptyenv())
  s
}

set_token <- function(session, token) {
  session$request <- list(HTTP_X_SHINYHUB_IDENTITY_TOKEN = token)
  invisible(session)
}

# Evaluate `expr` and return the shinyhub_identity_error it raised, or NULL.
catch_identity_error <- function(expr) {
  tryCatch(
    {
      expr
      NULL
    },
    shinyhub_identity_error = function(e) e
  )
}

expect_reason <- function(expr, reason) {
  err <- catch_identity_error(expr)
  testthat::expect_s3_class(err, "shinyhub_identity_error")
  testthat::expect_equal(err$reason, reason)
  invisible(err)
}
