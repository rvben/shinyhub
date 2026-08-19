# current_user verifies the handshake once per session. That is not an
# optimization: a Shiny session's request is frozen at the WebSocket handshake
# while the token it carries expires five minutes later, so re-verifying it
# from a reactive would start failing part-way through a long session even
# though nothing about the user changed. These tests pin that a later read
# cannot change the answer - and that the unhelped path does fail, so the
# once-per-session verification is provably doing the work.

test_that("the answer survives the handshake token expiring", {
  s <- fake_session(dmint())
  first <- current_user(s, key = dkey, slug = dslug)
  expect_equal(first$username, "alice")

  set_token(s, dmint(exp = as.numeric(Sys.time()) - 3600))

  # Positive control: verifying what the session now carries genuinely fails,
  # so the assertion below is not vacuous.
  expect_reason(
    verify_token(s$request$HTTP_X_SHINYHUB_IDENTITY_TOKEN, key = dkey, slug = dslug),
    "expired"
  )

  expect_equal(current_user(s, key = dkey, slug = dslug), first)
})

test_that("a rejected handshake keeps raising the same reason", {
  s <- fake_session(dmint(k = dwrong_key))
  expect_reason(current_user(s, key = dkey, slug = dslug), "bad_signature")

  # Even once a good token appears: the session was opened against a broken
  # deployment and must not silently start working.
  set_token(s, dmint())
  expect_reason(current_user(s, key = dkey, slug = dslug), "bad_signature")
})

test_that("anonymous is remembered too", {
  s <- fake_session()
  expect_null(current_user(s, key = dkey, slug = dslug))
  set_token(s, dmint())
  expect_null(current_user(s, key = dkey, slug = dslug))
})

test_that("sessions do not share an identity", {
  a <- fake_session(dmint(username = "alice"))
  b <- fake_session(dmint(username = "bob"))
  expect_equal(current_user(a, key = dkey, slug = dslug)$username, "alice")
  expect_equal(current_user(b, key = dkey, slug = dslug)$username, "bob")
  expect_equal(current_user(a, key = dkey, slug = dslug)$username, "alice")
})

test_that("a session-shaped list is verified on every call", {
  # Negative control for the two tests above: without a userData environment
  # there is nothing to remember the answer in, so a stub session sees each new
  # token. A stub's token does not age, which is why that is safe.
  s <- list(request = list(HTTP_X_SHINYHUB_IDENTITY_TOKEN = dmint(username = "alice")))
  expect_equal(current_user(s, key = dkey, slug = dslug)$username, "alice")
  s$request$HTTP_X_SHINYHUB_IDENTITY_TOKEN <- dmint(username = "bob")
  expect_equal(current_user(s, key = dkey, slug = dslug)$username, "bob")
})

test_that("the cache does not clutter the app's userData", {
  s <- fake_session(dmint())
  current_user(s, key = dkey, slug = dslug)
  # Dotted name, so an app's own ls(session$userData) is unaffected.
  expect_length(ls(s$userData), 0)
  expect_true(".shinyhub_identity" %in% ls(s$userData, all.names = TRUE))
})
