test_that("a valid token returns the identity", {
  u <- verify_token(dmint(), key = dkey, slug = dslug)
  expect_s3_class(u, "shinyhub_identity")
  expect_equal(u$username, "alice")
  expect_equal(u$user_id, "42")
  expect_equal(u$role, "admin")
  expect_equal(u$email, "alice@example.com")
  expect_equal(u$name, "Alice Liddell")
  expect_equal(u$groups, c("team-a", "team-b"))
  expect_false(u$groups_truncated)
})

test_that("the field names match the Python helper's Identity", {
  # Two SDKs, one documented contract: a field renamed on one side must fail
  # here rather than quietly making the shared docs wrong for R.
  u <- verify_token(dmint(), key = dkey, slug = dslug)
  expect_setequal(
    names(u),
    c(
      "user_id", "username", "role", "groups", "name", "email",
      "groups_truncated", "claims"
    )
  )
})

test_that("the raw claims stay available", {
  u <- verify_token(dmint(), key = dkey, slug = dslug)
  expect_equal(u$claims$iss, "shinyhub")
  expect_true(is.numeric(u$claims$exp))
})

test_that("absent email and name are empty strings, not NULL", {
  u <- verify_token(
    dmint(email = NULL, name = NULL),
    key = dkey, slug = dslug
  )
  expect_equal(u$email, "")
  expect_equal(u$name, "")
})

test_that("no groups is an empty character vector", {
  u <- verify_token(dmint(groups = list()), key = dkey, slug = dslug)
  expect_equal(u$groups, character(0))
})

test_that("current_user reads the session request header", {
  session <- list(request = list(HTTP_X_SHINYHUB_IDENTITY_TOKEN = dmint()))
  expect_equal(current_user(session, key = dkey, slug = dslug)$role, "admin")
})

test_that("a session with no token is anonymous", {
  expect_null(current_user(list(request = list()), key = dkey, slug = dslug))
})

test_that("no key and no token is anonymous (locally testable)", {
  with_env(list(SHINYHUB_IDENTITY_KEY = NA, SHINYHUB_APP_SLUG = NA), {
    expect_null(current_user(list(request = list())))
  })
})

test_that("key and slug default to the injected environment", {
  with_env(list(
    SHINYHUB_IDENTITY_KEY = sodium::bin2hex(dkey),
    SHINYHUB_APP_SLUG = dslug
  ), {
    expect_equal(verify_token(dmint())$username, "alice")
  })
})
