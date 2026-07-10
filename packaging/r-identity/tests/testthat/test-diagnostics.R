# A token that is PRESENT but rejected is almost always a misconfiguration (a
# genuine anonymous visitor sends no token at all), so that case must raise a
# warning - once per distinct reason per session - while the return value
# stays fail-closed (NULL) and a truly absent token stays silent.

test_that("a present token with a bad signature warns", {
  .shinyhub_reset_warned()
  wrong <- sodium::hex2bin(paste(rep("ff", 32), collapse = ""))
  expect_warning(
    expect_null(verify_token(dmint(k = wrong), key = dkey, slug = dslug)),
    "token present but rejected"
  )
})

test_that("a missing key env var warns and names it", {
  .shinyhub_reset_warned()
  with_env(list(SHINYHUB_IDENTITY_KEY = NA), {
    expect_warning(
      expect_null(verify_token(dmint(), slug = dslug)),
      "SHINYHUB_IDENTITY_KEY"
    )
  })
})

test_that("a non-hex key env var warns about hex", {
  .shinyhub_reset_warned()
  with_env(list(SHINYHUB_IDENTITY_KEY = "not-hex-at-all"), {
    expect_warning(
      expect_null(verify_token(dmint(), slug = dslug)),
      "hex"
    )
  })
})

test_that("a missing slug env var warns and names it", {
  .shinyhub_reset_warned()
  with_env(list(SHINYHUB_APP_SLUG = NA), {
    expect_warning(
      expect_null(verify_token(dmint(), key = dkey)),
      "SHINYHUB_APP_SLUG"
    )
  })
})

test_that("an expired token warns", {
  .shinyhub_reset_warned()
  stale <- dmint(exp = as.numeric(Sys.time()) - 3600)
  expect_warning(
    expect_null(verify_token(stale, key = dkey, slug = dslug)),
    "rejected"
  )
})

test_that("an absent token stays silent", {
  .shinyhub_reset_warned()
  expect_no_warning(expect_null(verify_token(NULL, key = dkey, slug = dslug)))
  expect_no_warning(expect_null(verify_token("", key = dkey, slug = dslug)))
})

test_that("the same reason warns only once per session", {
  .shinyhub_reset_warned()
  wrong <- sodium::hex2bin(paste(rep("ff", 32), collapse = ""))
  expect_warning(verify_token(dmint(k = wrong), key = dkey, slug = dslug))
  expect_no_warning(
    expect_null(verify_token(dmint(k = wrong), key = dkey, slug = dslug))
  )
})
