# The testing helpers must mint tokens the real verifier accepts, and must fail
# in exactly the ways the real one fails.
#
# A test helper for a security check has an unusual failure mode: if it is more
# permissive than production, every app that uses it writes green tests for code
# that breaks on deploy. So these tests are mostly about fidelity - the claim
# set, the omissions, the rejection reasons - rather than about convenience.
#
# Fidelity against the Go minter is proven separately, in
# internal/identity/conformance_test.go, which is the only place both minters
# exist at once.

# The claim set as it appears on the wire. Asserting on the decoded JSON rather
# than on parsed claims is deliberate: the distinction between an absent claim,
# a null one and an empty one is the whole point, and parsing blurs it.
payload_of <- function(token) {
  rawToChar(jose::base64url_decode(strsplit(token, ".", fixed = TRUE)[[1]][[2]]))
}

has_claim <- function(token, claim) {
  grepl(paste0("\"", claim, "\":"), payload_of(token), fixed = TRUE)
}

tkey <- shinyhub_test_key()
tslug <- shinyhub_test_slug()

# --- the point of the module: an app's signed-in path becomes testable --------

test_that("a minted token verifies", {
  user <- verify_token(shinyhub_test_token(username = "alice"), key = tkey, slug = tslug)
  expect_equal(user$username, "alice")
})

test_that("zero-configuration round trip", {
  # The headline ergonomic: no key handling, no fixtures, no environment setup
  # beyond the wrapper. If this breaks, the helper has failed at its actual job
  # even with every other test passing.
  user <- with_shinyhub_identity(
    current_user(shinyhub_test_session(username = "alice", groups = "admins"))
  )
  expect_equal(user$username, "alice")
  expect_equal(user$groups, "admins")
})

test_that("every identity field survives the round trip", {
  user <- with_shinyhub_identity(
    current_user(shinyhub_test_session(
      user_id = 7,
      username = "carol",
      role = "admin",
      email = "carol@example.com",
      name = "Carol Danvers",
      groups = c("b-team", "a-team"),
      groups_truncated = TRUE
    ))
  )
  expect_equal(user$user_id, "7") # sub is the numeric id, stringified
  expect_equal(user$username, "carol")
  expect_equal(user$role, "admin")
  expect_equal(user$email, "carol@example.com")
  expect_equal(user$name, "Carol Danvers")
  expect_equal(user$groups, c("a-team", "b-team")) # sorted, as the server sorts
  expect_true(user$groups_truncated)
})

test_that("an anonymous session is NULL, not an error", {
  expect_null(with_shinyhub_identity(
    current_user(shinyhub_test_session(token = ""))
  ))
})

# --- fidelity: the claim set must match what the proxy mints -----------------

test_that("empty optional claims are omitted, not empty strings", {
  # The Go claims carry omitempty, so a real token has no "email" key at all
  # when the IdP asserted none. An app doing claims$email must therefore see
  # NULL here exactly as it would in production, instead of "".
  token <- shinyhub_test_token()
  for (absent in c("email", "name", "app_role", "groups_truncated")) {
    expect_false(has_claim(token, absent), info = absent)
  }
})

test_that("always-present claims are always present", {
  token <- shinyhub_test_token()
  required <- c(
    "role", "groups", "preferred_username",
    "iss", "sub", "aud", "iat", "exp"
  )
  for (claim in required) {
    expect_true(has_claim(token, claim), info = claim)
  }
})

test_that("an empty group list mints as null", {
  # The server hands its minter a nil slice, which encodes as null rather than
  # []. Both read as "no groups", but only one is what production sends - and
  # in R the obvious spellings give neither: NULL would emit {} and
  # character(0) would emit [].
  expect_match(payload_of(shinyhub_test_token()), "\"groups\":null", fixed = TRUE)
})

test_that("groups mint as an array, sorted, even for one group", {
  expect_match(
    payload_of(shinyhub_test_token(groups = "solo")),
    "\"groups\":[\"solo\"]",
    fixed = TRUE
  )
  expect_match(
    payload_of(shinyhub_test_token(groups = c("b", "a"))),
    "\"groups\":[\"a\",\"b\"]",
    fixed = TRUE
  )
})

test_that("aud mints as an array", {
  # The server mints aud as a one-element list. jose::jwt_claim() refuses that
  # shape, so this is the assertion that keeps the hand-built claim list honest.
  expect_match(
    payload_of(shinyhub_test_token()),
    paste0("\"aud\":[\"", tslug, "\"]"),
    fixed = TRUE
  )
})

test_that("sub mints as a string", {
  expect_match(payload_of(shinyhub_test_token(user_id = 42)), "\"sub\":\"42\"", fixed = TRUE)
})

test_that("optional claims appear once set", {
  token <- shinyhub_test_token(
    email = "e@example.com", name = "N",
    app_role = "manager", groups_truncated = TRUE
  )
  claims <- verify_token(token, key = tkey, slug = tslug)$claims
  expect_equal(claims$email, "e@example.com")
  expect_equal(claims$name, "N")
  expect_equal(claims$app_role, "manager")
  expect_true(claims$groups_truncated)
})

test_that("token lifetime matches the server", {
  claims <- verify_token(shinyhub_test_token(), key = tkey, slug = tslug)$claims
  expect_equal(claims$exp - claims$iat, shinyhub_test_token_ttl())
})

test_that("iat and exp mint as plain integers", {
  # A double formatted in scientific notation, or with a decimal point, is a
  # valid JSON number that no other JWT library would produce.
  expect_match(payload_of(shinyhub_test_token()), "\"iat\":[0-9]+,")
  expect_match(payload_of(shinyhub_test_token()), "\"exp\":[0-9]+")
})

# --- fidelity: every rejection reason is reachable, and by one argument ------

test_that("each rejection reason is reachable by one argument", {
  # An app's error handling is only testable if every reason can be produced.
  expect_reason(
    verify_token(shinyhub_test_token(expires_in = -60), key = tkey, slug = tslug),
    "expired"
  )
  expect_reason(
    verify_token(shinyhub_test_token(key = as.raw(1:32)), key = tkey, slug = tslug),
    "bad_signature"
  )
  expect_reason(
    verify_token(shinyhub_test_token(slug = "some-other-app"), key = tkey, slug = tslug),
    "wrong_audience"
  )
  expect_reason(
    verify_token(shinyhub_test_token(issuer = "evil"), key = tkey, slug = tslug),
    "wrong_issuer"
  )
  expect_reason(verify_token("not-a-jwt-at-all", key = tkey, slug = tslug), "malformed")
})

test_that("the default token is genuinely valid", {
  # Positive control for the block above: if the defaults were themselves
  # broken, every rejection test would pass for the wrong reason.
  expect_s3_class(
    verify_token(shinyhub_test_token(), key = tkey, slug = tslug),
    "shinyhub_identity"
  )
})

# --- the helpers' own contracts ---------------------------------------------

test_that("with_shinyhub_identity restores a previous value", {
  with_env(list(SHINYHUB_APP_SLUG = "pre-existing"), {
    inner <- with_shinyhub_identity(Sys.getenv("SHINYHUB_APP_SLUG"))
    expect_equal(inner, tslug)
    expect_equal(Sys.getenv("SHINYHUB_APP_SLUG"), "pre-existing")
  })
})

test_that("with_shinyhub_identity restores absence", {
  with_env(list(SHINYHUB_IDENTITY_KEY = NA), {
    expect_true(with_shinyhub_identity(nzchar(Sys.getenv("SHINYHUB_IDENTITY_KEY"))))
    expect_equal(Sys.getenv("SHINYHUB_IDENTITY_KEY", unset = NA_character_), NA_character_)
  })
})

test_that("with_shinyhub_identity restores after a failure", {
  with_env(list(SHINYHUB_APP_SLUG = NA), {
    expect_error(with_shinyhub_identity(stop("boom")), "boom")
    expect_equal(Sys.getenv("SHINYHUB_APP_SLUG", unset = NA_character_), NA_character_)
  })
})

test_that("with_shinyhub_identity accepts an explicit key and slug", {
  other <- as.raw(1:32)
  user <- with_shinyhub_identity(
    current_user(shinyhub_test_session(key = other, slug = "my-app")),
    key = other, slug = "my-app"
  )
  expect_equal(user$username, "testuser")
})

test_that("a hex key is accepted wherever raw bytes are", {
  # ShinyHub hands apps the key as hex, so a test that copies that habit must
  # not need to convert it first.
  token <- shinyhub_test_token(key = sodium::bin2hex(tkey))
  expect_equal(verify_token(token, key = tkey, slug = tslug)$username, "testuser")
})

test_that("a test session caches like a real one", {
  # The session carries userData, so the handshake is verified once. Without it
  # the stub would re-verify every call and quietly not test what a real
  # session does.
  session <- with_shinyhub_identity(shinyhub_test_session(username = "alice"))
  first <- with_shinyhub_identity(current_user(session))
  expect_equal(first$username, "alice")

  stale <- shinyhub_test_token(username = "alice", expires_in = -3600)
  session$request <- list(HTTP_X_SHINYHUB_IDENTITY_TOKEN = stale)

  # Positive control: the token now in the request genuinely fails, so the
  # assertion below is not vacuous.
  expect_reason(verify_token(stale, key = tkey, slug = tslug), "expired")

  expect_equal(with_shinyhub_identity(current_user(session))$username, "alice")
})

test_that("a test session refuses ambiguous arguments", {
  # token= and minting arguments together would silently ignore one of them.
  expect_error(
    shinyhub_test_session(token = "abc", username = "alice"),
    "not both"
  )
})
