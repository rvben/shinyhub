"""Local-dev identity: with no ShinyHub proxy in front, current_user can
return a synthetic Identity from SHINYHUB_IDENTITY_DEV_* env vars so every app
does not have to invent its own mock. Guards make it impossible to trigger
under a real deployment: it only activates when no token arrived AND no
verification key exists (ShinyHub always injects SHINYHUB_IDENTITY_KEY)."""

import time

import jwt  # PyJWT
import pytest

from shinyhub_identity import Identity, current_user

KEY = bytes.fromhex("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
SLUG = "sales-dashboard"


@pytest.fixture(autouse=True)
def clean_env(monkeypatch):
    for var in (
        "SHINYHUB_IDENTITY_KEY",
        "SHINYHUB_APP_SLUG",
        "SHINYHUB_IDENTITY_DEV_USER",
        "SHINYHUB_IDENTITY_DEV_GROUPS",
        "SHINYHUB_IDENTITY_DEV_EMAIL",
        "SHINYHUB_IDENTITY_DEV_NAME",
        "SHINYHUB_IDENTITY_DEV_ROLE",
    ):
        monkeypatch.delenv(var, raising=False)
    return monkeypatch


def mint_for(slug, key):
    now = int(time.time())
    return jwt.encode(
        {"iss": "shinyhub", "sub": "42", "aud": slug, "preferred_username": "alice",
         "role": "admin", "groups": [], "iat": now, "exp": now + 300},
        key,
        algorithm="HS256",
    )


def test_dev_user_env_yields_synthetic_identity(clean_env):
    clean_env.setenv("SHINYHUB_IDENTITY_DEV_USER", "devlin")
    clean_env.setenv("SHINYHUB_IDENTITY_DEV_GROUPS", "team-a, team-b")
    clean_env.setenv("SHINYHUB_IDENTITY_DEV_EMAIL", "devlin@example.com")
    clean_env.setenv("SHINYHUB_IDENTITY_DEV_NAME", "Devlin Example")
    user = current_user({})
    assert isinstance(user, Identity)
    assert user.username == "devlin"
    assert user.user_id == "devlin"
    assert user.groups == ("team-a", "team-b")
    assert user.email == "devlin@example.com"
    assert user.name == "Devlin Example"
    assert user.groups_truncated is False


def test_dev_identity_is_marked_in_claims(clean_env):
    clean_env.setenv("SHINYHUB_IDENTITY_DEV_USER", "devlin")
    user = current_user({})
    assert user.claims["dev"] is True


def test_dev_role_defaults_to_viewer_and_is_overridable(clean_env):
    clean_env.setenv("SHINYHUB_IDENTITY_DEV_USER", "devlin")
    assert current_user({}).role == "viewer"
    clean_env.setenv("SHINYHUB_IDENTITY_DEV_ROLE", "admin")
    assert current_user({}).role == "admin"


def test_no_dev_env_means_anonymous(clean_env):
    assert current_user({}) is None


def test_dev_identity_never_activates_under_a_real_deployment(clean_env):
    # ShinyHub always injects SHINYHUB_IDENTITY_KEY into worker envs, so its
    # presence means "deployed": the dev identity must not mask a real
    # verification failure or a genuinely anonymous visitor there.
    clean_env.setenv("SHINYHUB_IDENTITY_DEV_USER", "devlin")
    clean_env.setenv("SHINYHUB_IDENTITY_KEY", KEY.hex())
    assert current_user({}) is None


def test_dev_identity_does_not_shadow_a_real_token(clean_env):
    clean_env.setenv("SHINYHUB_IDENTITY_DEV_USER", "devlin")
    headers = {"x-shinyhub-identity-token": mint_for(SLUG, KEY)}
    user = current_user(headers, key=KEY, slug=SLUG)
    assert user.username == "alice"


def test_dev_identity_skipped_when_key_passed_explicitly(clean_env):
    # An explicit key= means the caller is exercising the real verify path
    # (e.g. in tests); a missing token there must stay anonymous.
    clean_env.setenv("SHINYHUB_IDENTITY_DEV_USER", "devlin")
    assert current_user({}, key=KEY, slug=SLUG) is None


def test_dev_groups_parsing_ignores_blanks(clean_env):
    clean_env.setenv("SHINYHUB_IDENTITY_DEV_USER", "devlin")
    clean_env.setenv("SHINYHUB_IDENTITY_DEV_GROUPS", " team-a ,, team-b , ")
    assert current_user({}).groups == ("team-a", "team-b")
