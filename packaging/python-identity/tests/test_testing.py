"""The testing helpers must mint tokens the real verifier accepts, and must
fail in exactly the ways the real one fails.

A test helper for a security check has an unusual failure mode: if it is more
permissive than production, every app that uses it writes green tests for code
that breaks on deploy. So these tests are mostly about fidelity - the claim set,
the omissions, the rejection reasons - rather than about convenience.

Fidelity against the *Go* minter is proven separately, in
internal/identity/conformance_test.go, which is the only place both minters
exist at once."""

import os

import jwt  # PyJWT
import pytest

from shinyhub_identity import IdentityError, current_user, verify_token
from shinyhub_identity.shiny import session_identity
from shinyhub_identity.testing import (
    DEFAULT_KEY,
    DEFAULT_SLUG,
    TOKEN_TTL_SECONDS,
    fake_headers,
    fake_session,
    identity_env,
    mint_token,
)


def claims_of(token, *, key=DEFAULT_KEY, slug=DEFAULT_SLUG):
    """Raw claims, verified. Used to assert on shape rather than on Identity."""
    return jwt.decode(
        token, key, algorithms=["HS256"], audience=slug, issuer="shinyhub"
    )


# --- the point of the module: an app's signed-in path becomes testable --------


def test_a_minted_token_verifies():
    user = verify_token(mint_token(username="alice"), key=DEFAULT_KEY, slug=DEFAULT_SLUG)
    assert user.username == "alice"


def test_zero_configuration_round_trip():
    # The headline ergonomic: no key handling, no fixtures, no environment
    # setup beyond the context manager. If this breaks, the module has failed
    # at its actual job even with every other test passing.
    with identity_env():
        user = session_identity(fake_session(username="alice", groups=["admins"]))
    assert user.username == "alice"
    assert user.groups == ("admins",)


def test_current_user_accepts_fake_headers():
    with identity_env():
        assert current_user(fake_headers(username="bob")).username == "bob"


def test_every_identity_field_survives_the_round_trip():
    with identity_env():
        user = current_user(
            fake_headers(
                user_id=7,
                username="carol",
                role="admin",
                email="carol@example.com",
                name="Carol Danvers",
                groups=["b-team", "a-team"],
                groups_truncated=True,
            )
        )
    assert user.user_id == "7"          # sub is the numeric id, stringified
    assert user.username == "carol"
    assert user.role == "admin"
    assert user.email == "carol@example.com"
    assert user.name == "Carol Danvers"
    assert user.groups == ("a-team", "b-team")   # sorted, as the server sorts
    assert user.groups_truncated is True


def test_anonymous_session_is_none_not_an_error():
    with identity_env():
        assert session_identity(fake_session(token="")) is None


# --- fidelity: the claim set must match what the proxy mints -----------------


def test_empty_optional_claims_are_omitted_not_empty_strings():
    # The Go claims carry omitempty, so a real token has no "email" key at all
    # when the IdP asserted none. An app doing claims["email"] must therefore
    # fail here exactly as it would in production, instead of reading "".
    claims = claims_of(mint_token())
    for absent in ("email", "name", "app_role", "groups_truncated"):
        assert absent not in claims, f"{absent} must be omitted when empty"


def test_always_present_claims_are_always_present():
    claims = claims_of(mint_token())
    for required in ("role", "groups", "preferred_username", "iss", "sub", "aud", "iat", "exp"):
        assert required in claims, f"{required} is minted unconditionally by the server"


def test_an_empty_group_list_mints_as_null():
    # The server hands MintToken a nil slice, which encodes as null rather than
    # []. Both are read as "no groups", but only one is what production sends.
    assert claims_of(mint_token())["groups"] is None
    assert claims_of(mint_token(groups=["x"]))["groups"] == ["x"]


def test_optional_claims_appear_once_set():
    claims = claims_of(mint_token(email="e@example.com", name="N", app_role="manager", groups_truncated=True))
    assert claims["email"] == "e@example.com"
    assert claims["name"] == "N"
    assert claims["app_role"] == "manager"
    assert claims["groups_truncated"] is True


def test_token_lifetime_matches_the_server():
    claims = claims_of(mint_token())
    assert claims["exp"] - claims["iat"] == TOKEN_TTL_SECONDS


# --- fidelity: every rejection reason is reachable, and by one argument ------


@pytest.mark.parametrize(
    "reason, kwargs",
    [
        ("expired", {"expires_in": -60}),
        ("bad_signature", {"key": bytes(range(1, 33))}),
        ("wrong_audience", {"slug": "some-other-app"}),
        ("wrong_issuer", {"issuer": "evil"}),
    ],
)
def test_each_rejection_reason_is_reachable(reason, kwargs):
    # An app's error handling is only testable if every reason can be produced.
    # Each is one argument, so a test names the thing that is wrong.
    with identity_env():
        with pytest.raises(IdentityError) as caught:
            current_user(fake_headers(**kwargs))
    assert caught.value.reason == reason


def test_a_non_jwt_string_is_malformed():
    with identity_env():
        with pytest.raises(IdentityError) as caught:
            current_user(fake_headers(token="not-a-jwt-at-all"))
    assert caught.value.reason == "malformed"


def test_the_default_token_is_genuinely_valid():
    # Positive control for the table above: if the defaults were themselves
    # broken, every rejection test would pass for the wrong reason.
    with identity_env():
        assert current_user(fake_headers()) is not None


# --- the helpers' own contracts ---------------------------------------------


def test_identity_env_restores_a_previous_value():
    os.environ["SHINYHUB_APP_SLUG"] = "pre-existing"
    try:
        with identity_env():
            assert os.environ["SHINYHUB_APP_SLUG"] == DEFAULT_SLUG
        assert os.environ["SHINYHUB_APP_SLUG"] == "pre-existing"
    finally:
        os.environ.pop("SHINYHUB_APP_SLUG", None)


def test_identity_env_restores_absence():
    os.environ.pop("SHINYHUB_IDENTITY_KEY", None)
    with identity_env():
        assert "SHINYHUB_IDENTITY_KEY" in os.environ
    assert "SHINYHUB_IDENTITY_KEY" not in os.environ


def test_identity_env_restores_after_a_failure():
    os.environ.pop("SHINYHUB_APP_SLUG", None)
    with pytest.raises(ZeroDivisionError):
        with identity_env():
            1 / 0
    assert "SHINYHUB_APP_SLUG" not in os.environ


def test_identity_env_yields_the_key_and_slug_it_set():
    with identity_env() as (key, slug):
        assert key == DEFAULT_KEY
        assert slug == DEFAULT_SLUG
        assert os.environ["SHINYHUB_IDENTITY_KEY"] == key.hex()


def test_identity_env_accepts_an_explicit_key_and_slug():
    with identity_env(key=bytes(range(1, 33)), slug="my-app") as (key, slug):
        assert slug == "my-app"
        assert current_user(fake_headers(key=key, slug="my-app")).username == "testuser"


def test_a_hex_key_is_accepted_wherever_bytes_are():
    # ShinyHub hands apps the key as hex, so a test that copies that habit must
    # not need to convert it first.
    token = mint_token(key=DEFAULT_KEY.hex())
    assert verify_token(token, key=DEFAULT_KEY, slug=DEFAULT_SLUG).username == "testuser"


def test_fake_session_is_weak_referenceable():
    # session_identity refuses a session it cannot weakly reference, so a stub
    # that is not would be rejected by the very function it exists to feed.
    import weakref

    assert weakref.ref(fake_session()) is not None


def test_fake_session_caches_like_a_real_one():
    with identity_env():
        session = fake_session(username="alice")
        first = session_identity(session)
        assert session_identity(session) == first


def test_fake_headers_uses_the_header_name_the_proxy_sends():
    assert list(fake_headers()) == ["x-shinyhub-identity-token"]


def test_fake_headers_refuses_ambiguous_arguments():
    # token= and mint arguments together would silently ignore one of them.
    with pytest.raises(TypeError):
        fake_headers(token="abc", username="alice")


def test_anonymous_needs_no_helper():
    # Documented contract: {} is an anonymous request. Pinned so the docstring
    # cannot drift from the behaviour.
    with identity_env():
        assert current_user({}) is None
