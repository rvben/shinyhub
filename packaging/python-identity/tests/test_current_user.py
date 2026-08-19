import time

import jwt  # PyJWT
import pytest

from shinyhub_identity import Identity, IdentityError, current_user, verify_token

# 32-byte key, matching ShinyHub's HKDF-SHA256-derived per-app key length.
KEY = bytes.fromhex("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
SLUG = "sales-dashboard"


def mint(
    *,
    key=KEY,
    slug=SLUG,
    sub="42",
    username="alice",
    role="admin",
    email="alice@example.com",
    name="Alice Liddell",
    groups=("team-a", "team-b"),
    iss="shinyhub",
    exp_delta=300,
    iat=None,
):
    now = iat if iat is not None else int(time.time())
    claims = {
        "iss": iss,
        "sub": sub,
        "aud": slug,
        "preferred_username": username,
        "role": role,
        "groups": list(groups),
        "iat": now,
        "exp": now + exp_delta,
    }
    if email is not None:
        claims["email"] = email
    if name is not None:
        claims["name"] = name
    return jwt.encode(claims, key, algorithm="HS256")


def headers(token):
    return {"x-shinyhub-identity-token": token}


def test_valid_token_returns_identity():
    u = current_user(headers(mint()), key=KEY, slug=SLUG)
    assert isinstance(u, Identity)
    assert u.username == "alice"
    assert u.user_id == "42"
    assert u.email == "alice@example.com"
    assert u.role == "admin"
    assert u.groups == ("team-a", "team-b")
    assert u.groups_truncated is False


def test_email_absent_is_empty_string():
    u = current_user(headers(mint(email=None)), key=KEY, slug=SLUG)
    assert u.email == ""


def test_valid_token_exposes_name():
    u = current_user(headers(mint()), key=KEY, slug=SLUG)
    assert u.name == "Alice Liddell"


def test_name_absent_is_empty_string():
    u = current_user(headers(mint(name=None)), key=KEY, slug=SLUG)
    assert u.name == ""


def test_identity_is_keyword_only():
    # Field order is not part of the API: constructing positionally must fail,
    # so a future field can be inserted anywhere without breaking callers.
    with pytest.raises(TypeError):
        Identity("42", "alice", "admin")


def test_missing_token_is_anonymous():
    assert current_user({}, key=KEY, slug=SLUG) is None


def test_present_but_invalid_token_is_not_anonymous():
    # The whole point of the contract: a broken deployment must not render as
    # a logged-out visitor.
    wrong = bytes.fromhex("ff" * 32)
    with pytest.raises(IdentityError):
        current_user(headers(mint(key=wrong)), key=KEY, slug=SLUG)


def test_canonical_case_header_key_is_found():
    # Not every framework lowercases header keys; a canonical-case dict must work.
    h = {"X-Shinyhub-Identity-Token": mint()}
    assert current_user(h, key=KEY, slug=SLUG).username == "alice"


def test_no_env_and_no_token_is_anonymous(monkeypatch):
    # App run locally with no ShinyHub in front: no crash, defined anonymous.
    monkeypatch.delenv("SHINYHUB_IDENTITY_KEY", raising=False)
    monkeypatch.delenv("SHINYHUB_APP_SLUG", raising=False)
    assert current_user({}) is None


def test_reads_key_and_slug_from_env(monkeypatch):
    monkeypatch.setenv("SHINYHUB_IDENTITY_KEY", KEY.hex())
    monkeypatch.setenv("SHINYHUB_APP_SLUG", SLUG)
    assert current_user(headers(mint())).role == "admin"


def test_verify_token_primitive():
    assert verify_token(mint(), key=KEY, slug=SLUG).username == "alice"


def test_verify_token_rejects_an_absent_token():
    # Unlike current_user, this primitive has no "anonymous" outcome: the
    # caller asked to verify something and passed nothing.
    for empty in ("", None):
        with pytest.raises(IdentityError) as caught:
            verify_token(empty, key=KEY, slug=SLUG)
        assert caught.value.reason == "no_token"
