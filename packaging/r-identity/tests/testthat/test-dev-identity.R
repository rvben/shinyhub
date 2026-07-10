# Local-dev identity: with no ShinyHub proxy in front, current_user can return
# a synthetic identity from SHINYHUB_IDENTITY_DEV_* env vars. Guards make it
# impossible to trigger under a real deployment: it only activates when no
# token arrived AND no verification key exists (ShinyHub always injects
# SHINYHUB_IDENTITY_KEY into app processes).

test_that("dev env vars yield a synthetic identity", {
  with_env(list(
    SHINYHUB_IDENTITY_KEY = NA,
    SHINYHUB_IDENTITY_DEV_USER = "devlin",
    SHINYHUB_IDENTITY_DEV_GROUPS = "team-a, team-b",
    SHINYHUB_IDENTITY_DEV_EMAIL = "devlin@example.com",
    SHINYHUB_IDENTITY_DEV_NAME = "Devlin Example",
    SHINYHUB_IDENTITY_DEV_ROLE = NA
  ), {
    u <- current_user(list(request = list()))
    expect_equal(u$preferred_username, "devlin")
    expect_equal(u$role, "viewer")
    expect_true("team-a" %in% u$groups)
    expect_true("team-b" %in% u$groups)
    expect_equal(u$email, "devlin@example.com")
    expect_equal(u$name, "Devlin Example")
    expect_true(isTRUE(u$dev))
  })
})

test_that("dev role is overridable", {
  with_env(list(
    SHINYHUB_IDENTITY_KEY = NA,
    SHINYHUB_IDENTITY_DEV_USER = "devlin",
    SHINYHUB_IDENTITY_DEV_ROLE = "admin"
  ), {
    expect_equal(current_user(list(request = list()))$role, "admin")
  })
})

test_that("dev identity never activates when the real key is present", {
  with_env(list(
    SHINYHUB_IDENTITY_KEY = strrep("00", 32),
    SHINYHUB_IDENTITY_DEV_USER = "devlin"
  ), {
    expect_null(current_user(list(request = list())))
  })
})

test_that("dev identity does not shadow a real token", {
  with_env(list(
    SHINYHUB_IDENTITY_KEY = NA,
    SHINYHUB_IDENTITY_DEV_USER = "devlin"
  ), {
    session <- list(request = list(HTTP_X_SHINYHUB_IDENTITY_TOKEN = dmint()))
    u <- current_user(session, key = dkey, slug = dslug)
    expect_equal(u$preferred_username, "alice")
  })
})

test_that("dev identity is skipped when a key is passed explicitly", {
  with_env(list(
    SHINYHUB_IDENTITY_KEY = NA,
    SHINYHUB_IDENTITY_DEV_USER = "devlin"
  ), {
    expect_null(current_user(list(request = list()), key = dkey, slug = dslug))
  })
})

test_that("absent dev env means anonymous", {
  with_env(list(
    SHINYHUB_IDENTITY_KEY = NA,
    SHINYHUB_APP_SLUG = NA,
    SHINYHUB_IDENTITY_DEV_USER = NA
  ), {
    expect_null(current_user(list(request = list())))
  })
})
