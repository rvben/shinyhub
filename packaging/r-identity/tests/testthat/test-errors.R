# A token that is PRESENT but rejected is a broken deployment, not a visitor (a
# genuine anonymous visitor sends no token at all), so it raises a
# shinyhub_identity_error carrying a machine-readable reason instead of
# collapsing into the same NULL an anonymous request produces. These tests pin
# the reason vocabulary, which the Python helper mirrors.

test_that("a bad signature is bad_signature", {
  expect_reason(verify_token(dmint(k = dwrong_key), key = dkey, slug = dslug), "bad_signature")
})

test_that("an expired token is expired", {
  stale <- dmint(exp = as.numeric(Sys.time()) - 3600)
  expect_reason(verify_token(stale, key = dkey, slug = dslug), "expired")
})

test_that("a wrong audience is wrong_audience", {
  expect_reason(verify_token(dmint(s = "other-app"), key = dkey, slug = dslug), "wrong_audience")
})

test_that("a wrong issuer is wrong_issuer", {
  expect_reason(verify_token(dmint(iss = "evil"), key = dkey, slug = dslug), "wrong_issuer")
})

test_that("an unparseable token is malformed", {
  expect_reason(verify_token("not-a-jwt-at-all", key = dkey, slug = dslug), "malformed")
})

test_that("a token missing exp is malformed", {
  # jwt_claim() without exp produces a claim set carrying no expiry (jose adds
  # iat but not exp); a signed token without an expiry must not bypass the
  # short-lived-token guarantee.
  no_exp <- jose::jwt_claim(
    iss = "shinyhub", sub = "42", aud = dslug,
    preferred_username = "alice", role = "admin", groups = list("team-a")
  )
  token <- jose::jwt_encode_hmac(no_exp, secret = dkey)
  expect_reason(verify_token(token, key = dkey, slug = dslug), "malformed")
})

test_that("a token missing iss or aud is malformed, not wrong_issuer/wrong_audience", {
  # A claim ShinyHub always mints but this token lacks means the token is not a
  # ShinyHub token at all. The Python helper reports malformed for both (PyJWT
  # raises MissingRequiredClaimError), so reporting "wrong issuer" here would
  # hand the two SDKs different vocabularies for one token.
  drop_claim <- function(name) {
    cl <- list(
      iss = "shinyhub", sub = "42", aud = dslug, preferred_username = "alice",
      role = "admin", exp = as.numeric(Sys.time()) + 300
    )
    cl[[name]] <- NULL
    jose::jwt_encode_hmac(do.call(jose::jwt_claim, cl), secret = dkey)
  }
  expect_reason(verify_token(drop_claim("iss"), key = dkey, slug = dslug), "malformed")
  expect_reason(verify_token(drop_claim("aud"), key = dkey, slug = dslug), "malformed")
})

test_that("an algorithm other than HS256 is malformed", {
  # jose verifies whatever HMAC size the token's own header asks for, so an
  # HS512 token signed with the same key would otherwise be accepted here and
  # rejected by the Python helper's algorithms=["HS256"].
  hs512 <- jose::jwt_encode_hmac(
    jose::jwt_claim(
      iss = "shinyhub", sub = "42", aud = dslug, preferred_username = "alice",
      role = "admin", exp = as.numeric(Sys.time()) + 300
    ),
    secret = dkey, size = 512
  )
  err <- expect_reason(verify_token(hs512, key = dkey, slug = dslug), "malformed")
  expect_match(conditionMessage(err), "HS512", fixed = TRUE)

  b64url <- function(s) jose::base64url_encode(charToRaw(s))
  unsigned <- paste0(
    b64url('{"alg":"none","typ":"JWT"}'), ".",
    b64url(sprintf(
      '{"iss":"shinyhub","aud":"%s","sub":"42","preferred_username":"mallory","role":"admin","exp":%d}',
      dslug, as.integer(as.numeric(Sys.time()) + 300)
    )), "."
  )
  expect_reason(verify_token(unsigned, key = dkey, slug = dslug), "malformed")
})

test_that("expiry is enforced at `leeway`, not at jose's wider grace", {
  # jose applies a fixed 60-second grace of its own. Without an explicit check a
  # token 45 seconds past exp is accepted here and rejected by the Python
  # helper, whose default leeway is 30 seconds: the same token, two answers.
  just_expired <- dmint(exp = as.numeric(Sys.time()) - 45)
  expect_reason(verify_token(just_expired, key = dkey, slug = dslug), "expired")

  # jose alone still accepts it, which is what makes the check load-bearing.
  expect_type(jose::jwt_decode_hmac(just_expired, secret = dkey), "list")

  # Inside the leeway, both SDKs accept.
  fresh <- dmint(exp = as.numeric(Sys.time()) - 20)
  expect_s3_class(verify_token(fresh, key = dkey, slug = dslug), "shinyhub_identity")

  # And leeway is a real dial, not a constant.
  expect_reason(verify_token(fresh, key = dkey, slug = dslug, leeway = 5), "expired")
})

test_that("a missing key env var is no_key and names the variable", {
  with_env(list(SHINYHUB_IDENTITY_KEY = NA), {
    err <- expect_reason(verify_token(dmint(), slug = dslug), "no_key")
    expect_match(conditionMessage(err), "SHINYHUB_IDENTITY_KEY", fixed = TRUE)
  })
})

test_that("a non-hex key env var is bad_key and says hex", {
  with_env(list(SHINYHUB_IDENTITY_KEY = "not-hex-at-all"), {
    err <- expect_reason(verify_token(dmint(), slug = dslug), "bad_key")
    expect_match(conditionMessage(err), "hex", fixed = TRUE)
  })
})

test_that("an explicitly passed empty key is no_key, not bad_key or bad_signature", {
  # An app that reads the variable itself and passes it through gets an empty
  # value when it is unset. The reason must not depend on which of the two
  # routes the empty key travelled, and must not accuse a key that was never
  # there of being the wrong key: "" once reported bad_key and raw(0) reported
  # bad_signature, sending the operator to rotate rather than to set the env.
  for (empty in list("", raw(0), character(0))) {
    err <- expect_reason(verify_token(dmint(), key = empty, slug = dslug), "no_key")
    expect_match(conditionMessage(err), "SHINYHUB_IDENTITY_KEY", fixed = TRUE)
  }

  # A key that IS present and simply wrong still reports the wrong key.
  expect_reason(verify_token(dmint(), key = dwrong_key, slug = dslug), "bad_signature")
})

test_that("a missing slug env var is no_slug and names the variable", {
  with_env(list(SHINYHUB_APP_SLUG = NA), {
    err <- expect_reason(verify_token(dmint(), key = dkey), "no_slug")
    expect_match(conditionMessage(err), "SHINYHUB_APP_SLUG", fixed = TRUE)
  })
})

test_that("verify_token rejects an absent token", {
  # Unlike current_user, this primitive has no "anonymous" outcome: the caller
  # asked to verify something and passed nothing.
  expect_reason(verify_token(NULL, key = dkey, slug = dslug), "no_token")
  expect_reason(verify_token("", key = dkey, slug = dslug), "no_token")
})

test_that("current_user propagates the same reason", {
  session <- list(request = list(HTTP_X_SHINYHUB_IDENTITY_TOKEN = dmint(k = dwrong_key)))
  expect_reason(current_user(session, key = dkey, slug = dslug), "bad_signature")
})

test_that("an absent token never raises", {
  # The one case that is genuinely anonymous stays anonymous, and quiet.
  expect_null(current_user(list(request = list()), key = dkey, slug = dslug))
  expect_null(current_user(
    list(request = list(HTTP_X_SHINYHUB_IDENTITY_TOKEN = "")),
    key = dkey, slug = dslug
  ))
})

test_that("the condition carries reason and detail separately", {
  err <- expect_reason(verify_token(dmint(k = dwrong_key), key = dkey, slug = dslug), "bad_signature")
  # reason is for code to branch on, detail is for the operator's log line; the
  # message carries both so an uncaught error is self-explanatory.
  expect_true(nzchar(err$detail))
  expect_match(conditionMessage(err), err$reason, fixed = TRUE)
  expect_match(conditionMessage(err), err$detail, fixed = TRUE)
})

test_that("the jose messages the reason classifier reads are unchanged", {
  # .shinyhub_decode_reason recovers the reason from jose's error text, because
  # jose signals every rejection as a plain error. Pin the wording here: if a
  # jose release rewords one of these, this fails loudly instead of every
  # rejection silently degrading to "malformed".
  msg <- function(token) {
    tryCatch(
      {
        jose::jwt_decode_hmac(token, secret = dkey)
        NA_character_
      },
      error = conditionMessage
    )
  }

  bad_sig <- msg(dmint(k = dwrong_key))
  expired <- msg(dmint(exp = as.numeric(Sys.time()) - 3600))
  garbage <- msg("not-a-jwt-at-all")

  expect_match(bad_sig, "signature", ignore.case = TRUE)
  expect_match(expired, "has expired", fixed = TRUE)

  expect_equal(.shinyhub_decode_reason(bad_sig), "bad_signature")
  expect_equal(.shinyhub_decode_reason(expired), "expired")
  expect_equal(.shinyhub_decode_reason(garbage), "malformed")
})
