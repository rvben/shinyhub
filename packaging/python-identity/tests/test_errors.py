"""A token that is PRESENT but rejected is a broken deployment, not a visitor
(a genuine anonymous visitor sends no token at all), so it raises IdentityError
with a machine-readable reason instead of collapsing into the same None an
anonymous request produces. These tests pin the reason vocabulary, which the R
helper mirrors."""

import time

import jwt  # PyJWT
import pytest

from shinyhub_identity import IdentityError, current_user, verify_token

KEY = bytes.fromhex("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
WRONG_KEY = bytes.fromhex("ff" * 32)
SLUG = "sales-dashboard"


def mint(*, key=KEY, slug=SLUG, iss="shinyhub", exp_delta=300):
    now = int(time.time())
    return jwt.encode(
        {
            "iss": iss,
            "sub": "42",
            "aud": slug,
            "preferred_username": "alice",
            "role": "admin",
            "groups": [],
            "iat": now,
            "exp": now + exp_delta,
        },
        key,
        algorithm="HS256",
    )


def reason_of(call):
    with pytest.raises(IdentityError) as caught:
        call()
    return caught.value


def test_bad_signature():
    err = reason_of(lambda: verify_token(mint(key=WRONG_KEY), key=KEY, slug=SLUG))
    assert err.reason == "bad_signature"


def test_expired():
    err = reason_of(lambda: verify_token(mint(exp_delta=-3600), key=KEY, slug=SLUG))
    assert err.reason == "expired"


def test_wrong_audience():
    err = reason_of(lambda: verify_token(mint(slug="other-app"), key=KEY, slug=SLUG))
    assert err.reason == "wrong_audience"


def test_wrong_issuer():
    err = reason_of(lambda: verify_token(mint(iss="evil"), key=KEY, slug=SLUG))
    assert err.reason == "wrong_issuer"


def test_malformed():
    err = reason_of(lambda: verify_token("not-a-jwt-at-all", key=KEY, slug=SLUG))
    assert err.reason == "malformed"


def test_missing_exp_is_malformed():
    # A signed token that omits exp must not be accepted: the short-lived-token
    # guarantee requires an expiry to bound replay.
    token = jwt.encode(
        {"iss": "shinyhub", "sub": "42", "aud": SLUG, "role": "admin"},
        KEY,
        algorithm="HS256",
    )
    err = reason_of(lambda: verify_token(token, key=KEY, slug=SLUG))
    assert err.reason == "malformed"


def test_missing_env_key_names_the_variable(monkeypatch):
    monkeypatch.delenv("SHINYHUB_IDENTITY_KEY", raising=False)
    err = reason_of(lambda: verify_token(mint(), slug=SLUG))
    assert err.reason == "no_key"
    assert "SHINYHUB_IDENTITY_KEY" in str(err)


def test_non_hex_env_key(monkeypatch):
    monkeypatch.setenv("SHINYHUB_IDENTITY_KEY", "not-hex-at-all")
    err = reason_of(lambda: verify_token(mint(), slug=SLUG))
    assert err.reason == "bad_key"
    assert "hex" in str(err)


def test_missing_slug_names_the_variable(monkeypatch):
    monkeypatch.delenv("SHINYHUB_APP_SLUG", raising=False)
    err = reason_of(lambda: verify_token(mint(), key=KEY))
    assert err.reason == "no_slug"
    assert "SHINYHUB_APP_SLUG" in str(err)


def test_current_user_propagates_the_same_reason():
    headers = {"x-shinyhub-identity-token": mint(key=WRONG_KEY)}
    err = reason_of(lambda: current_user(headers, key=KEY, slug=SLUG))
    assert err.reason == "bad_signature"


def test_absent_token_never_raises():
    # The one case that is genuinely anonymous stays anonymous, and quiet.
    assert current_user({}, key=KEY, slug=SLUG) is None
    assert current_user({"x-shinyhub-identity-token": ""}, key=KEY, slug=SLUG) is None


def test_error_carries_reason_and_detail_separately():
    err = reason_of(lambda: verify_token(mint(key=WRONG_KEY), key=KEY, slug=SLUG))
    # reason is for code to branch on, detail is for the operator's log line;
    # the message carries both so an uncaught error is self-explanatory.
    assert err.reason == "bad_signature"
    assert err.detail
    assert err.reason in str(err) and err.detail in str(err)
