# 32-byte key, matching ShinyHub's HKDF-SHA256-derived per-app key length.
key <- sodium::hex2bin(paste0(
  "00112233445566778899aabbccddeeff",
  "00112233445566778899aabbccddeeff"
))
slug <- "sales-dashboard"

mint <- function(k = key, s = slug, iss = "shinyhub", role = "admin",
                 groups = list("team-a", "team-b"), username = "alice",
                 email = "alice@example.com", name = "Alice Liddell", sub = "42",
                 exp = as.numeric(Sys.time()) + 300) {
  claims <- jose::jwt_claim(
    iss = iss, sub = sub, aud = s,
    preferred_username = username, role = role, email = email, name = name,
    groups = groups, exp = exp
  )
  jose::jwt_encode_hmac(claims, secret = k)
}

test_that("a valid token returns the identity", {
  u <- verify_token(mint(), key = key, slug = slug)
  expect_equal(u$preferred_username, "alice")
  expect_equal(u$role, "admin")
  expect_equal(u$email, "alice@example.com")
  expect_equal(u$name, "Alice Liddell")
  expect_equal(as.character(u$sub), "42")
})

test_that("a missing token is anonymous", {
  expect_null(verify_token(NULL, key = key, slug = slug))
  expect_null(verify_token("", key = key, slug = slug))
})

# The negative-path tests below assert only the fail-closed NULL return and
# suppress the present-but-rejected warning; the warning contract itself is
# asserted in test-diagnostics.R.

test_that("a bad signature is anonymous", {
  wrong <- sodium::hex2bin(paste(rep("ff", 32), collapse = ""))
  expect_null(suppressWarnings(verify_token(mint(k = wrong), key = key, slug = slug)))
})

test_that("a wrong audience is anonymous", {
  expect_null(suppressWarnings(verify_token(mint(s = "other-app"), key = key, slug = slug)))
})

test_that("a wrong issuer is anonymous", {
  expect_null(suppressWarnings(verify_token(mint(iss = "evil"), key = key, slug = slug)))
})

test_that("an expired token is anonymous", {
  stale <- mint(exp = as.numeric(Sys.time()) - 3600)
  expect_null(suppressWarnings(verify_token(stale, key = key, slug = slug)))
})

test_that("a token missing exp is anonymous", {
  # Plain claim list (not jwt_claim) so no exp is added; a signed token without
  # an expiry must not bypass the short-lived-token guarantee.
  # jwt_claim() without exp produces a jwt_claim carrying no expiry (jose adds
  # iat but not exp); a signed token without an expiry must not bypass the
  # short-lived-token guarantee.
  no_exp <- jose::jwt_claim(
    iss = "shinyhub", sub = "42", aud = slug,
    preferred_username = "alice", role = "admin", groups = list("team-a")
  )
  token <- jose::jwt_encode_hmac(no_exp, secret = key)
  expect_null(suppressWarnings(verify_token(token, key = key, slug = slug)))
})

test_that("current_user reads the session request header", {
  session <- list(request = list(HTTP_X_SHINYHUB_IDENTITY_TOKEN = mint()))
  expect_equal(current_user(session, key = key, slug = slug)$role, "admin")
})

test_that("no key and no token is anonymous (locally testable)", {
  old_key <- Sys.getenv("SHINYHUB_IDENTITY_KEY", unset = NA)
  old_slug <- Sys.getenv("SHINYHUB_APP_SLUG", unset = NA)
  Sys.unsetenv("SHINYHUB_IDENTITY_KEY")
  Sys.unsetenv("SHINYHUB_APP_SLUG")
  on.exit({
    if (!is.na(old_key)) Sys.setenv(SHINYHUB_IDENTITY_KEY = old_key)
    if (!is.na(old_slug)) Sys.setenv(SHINYHUB_APP_SLUG = old_slug)
  })
  expect_null(current_user(list(request = list())))
})
