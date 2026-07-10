"""A token that is PRESENT but rejected is almost always a misconfiguration
(a genuine anonymous visitor sends no token at all), so that case must emit a
WARNING on the "shinyhub_identity" logger - once per distinct reason per
process - while a truly absent token stays silent and everything keeps
returning None (fail-closed)."""

import logging
import time

import jwt  # PyJWT
import pytest

import shinyhub_identity
from shinyhub_identity import current_user, verify_token

KEY = bytes.fromhex("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
WRONG_KEY = bytes.fromhex("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
SLUG = "sales-dashboard"


def mint(*, key=KEY, slug=SLUG, exp_delta=300):
    now = int(time.time())
    claims = {
        "iss": "shinyhub",
        "sub": "42",
        "aud": slug,
        "preferred_username": "alice",
        "role": "admin",
        "groups": [],
        "iat": now,
        "exp": now + exp_delta,
    }
    return jwt.encode(claims, key, algorithm="HS256")


@pytest.fixture(autouse=True)
def reset_warn_dedup():
    shinyhub_identity._warned.clear()
    yield
    shinyhub_identity._warned.clear()


def rejected_records(caplog):
    return [
        r
        for r in caplog.records
        if r.name == "shinyhub_identity" and "rejected" in r.getMessage()
    ]


def test_bad_signature_warns(caplog):
    with caplog.at_level(logging.WARNING, logger="shinyhub_identity"):
        assert verify_token(mint(key=WRONG_KEY), key=KEY, slug=SLUG) is None
    recs = rejected_records(caplog)
    assert len(recs) == 1
    assert "token present but rejected" in recs[0].getMessage()


def test_expired_token_warns_with_reason(caplog):
    with caplog.at_level(logging.WARNING, logger="shinyhub_identity"):
        assert verify_token(mint(exp_delta=-3600), key=KEY, slug=SLUG) is None
    recs = rejected_records(caplog)
    assert len(recs) == 1
    assert "expired" in recs[0].getMessage().lower()


def test_missing_env_key_warns_and_names_the_variable(caplog, monkeypatch):
    monkeypatch.delenv("SHINYHUB_IDENTITY_KEY", raising=False)
    with caplog.at_level(logging.WARNING, logger="shinyhub_identity"):
        assert verify_token(mint(), slug=SLUG) is None
    recs = rejected_records(caplog)
    assert len(recs) == 1
    assert "SHINYHUB_IDENTITY_KEY" in recs[0].getMessage()


def test_non_hex_env_key_warns(caplog, monkeypatch):
    monkeypatch.setenv("SHINYHUB_IDENTITY_KEY", "not-hex-at-all")
    with caplog.at_level(logging.WARNING, logger="shinyhub_identity"):
        assert verify_token(mint(), slug=SLUG) is None
    recs = rejected_records(caplog)
    assert len(recs) == 1
    assert "hex" in recs[0].getMessage()


def test_missing_slug_warns(caplog, monkeypatch):
    monkeypatch.delenv("SHINYHUB_APP_SLUG", raising=False)
    with caplog.at_level(logging.WARNING, logger="shinyhub_identity"):
        assert verify_token(mint(), key=KEY) is None
    recs = rejected_records(caplog)
    assert len(recs) == 1
    assert "SHINYHUB_APP_SLUG" in recs[0].getMessage()


def test_absent_token_stays_silent(caplog):
    with caplog.at_level(logging.WARNING, logger="shinyhub_identity"):
        assert current_user({}, key=KEY, slug=SLUG) is None
        assert verify_token(None, key=KEY, slug=SLUG) is None
        assert verify_token("", key=KEY, slug=SLUG) is None
    assert rejected_records(caplog) == []


def test_same_reason_warns_only_once(caplog):
    with caplog.at_level(logging.WARNING, logger="shinyhub_identity"):
        assert verify_token(mint(key=WRONG_KEY), key=KEY, slug=SLUG) is None
        assert verify_token(mint(key=WRONG_KEY), key=KEY, slug=SLUG) is None
    assert len(rejected_records(caplog)) == 1


def test_distinct_reasons_each_warn(caplog):
    with caplog.at_level(logging.WARNING, logger="shinyhub_identity"):
        assert verify_token(mint(key=WRONG_KEY), key=KEY, slug=SLUG) is None
        assert verify_token(mint(exp_delta=-3600), key=KEY, slug=SLUG) is None
    assert len(rejected_records(caplog)) == 2


def test_current_user_wires_the_same_warning(caplog):
    headers = {"x-shinyhub-identity-token": mint(key=WRONG_KEY)}
    with caplog.at_level(logging.WARNING, logger="shinyhub_identity"):
        assert current_user(headers, key=KEY, slug=SLUG) is None
    assert len(rejected_records(caplog)) == 1
