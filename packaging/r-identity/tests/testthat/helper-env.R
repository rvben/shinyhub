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
dslug <- "sales-dashboard"

dmint <- function(k = dkey, s = dslug, exp = as.numeric(Sys.time()) + 300) {
  claims <- jose::jwt_claim(
    iss = "shinyhub", sub = "42", aud = s,
    preferred_username = "alice", role = "admin",
    groups = list("team-a"), exp = exp
  )
  jose::jwt_encode_hmac(claims, secret = k)
}
