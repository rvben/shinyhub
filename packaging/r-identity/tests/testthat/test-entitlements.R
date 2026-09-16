test_that("entitlements remain separate from groups and older tokens work", {
  user <- verify_token(
    shinyhub_test_token(groups = "finance", entitlements = "power_user"),
    key = shinyhub_test_key(), slug = shinyhub_test_slug()
  )
  expect_identical(user$entitlements, "power_user")
  expect_identical(user$groups, "finance")
  expect_identical(user$role, "viewer")
  old <- verify_token(shinyhub_test_token(), key = shinyhub_test_key(), slug = shinyhub_test_slug())
  expect_identical(old$entitlements, character(0))
})

test_that("malformed entitlement claims fail verification", {
  for (value in list(NA, "power_user", list(role = "power_user"), list(1), list(""))) {
    claims <- jose::jwt_split(shinyhub_test_token())$payload
    claims$entitlements <- value
    claims$aud <- list(shinyhub_test_slug())
    token <- jose::jwt_encode_hmac(structure(claims, class = c("jwt_claim", "list")), shinyhub_test_key())
    error <- tryCatch(
      verify_token(token, key = shinyhub_test_key(), slug = shinyhub_test_slug()),
      shinyhub_identity_error = identity
    )
    expect_s3_class(error, "shinyhub_identity_error")
    expect_identical(error$reason, "malformed")
  }
})
