"""session_identity takes a Shiny session and verifies once for its lifetime.

The cache is not an optimization. ShinyHub binds identity at the WebSocket
handshake and the forwarded token expires 5 minutes later, so an app that
re-verifies the handshake headers from a reactive starts failing part-way
through a long session even though the user has not changed. These tests pin
that a later read cannot change the answer - and that the unhelped path does
fail, so the cache is provably doing the work."""

import time

import jwt  # PyJWT
import pytest

from shinyhub_identity import IdentityError, current_user
from shinyhub_identity.shiny import session_identity

KEY = bytes.fromhex("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
WRONG_KEY = bytes.fromhex("ff" * 32)
SLUG = "sales-dashboard"


def mint(*, key=KEY, slug=SLUG, username="alice", exp_delta=300):
    now = int(time.time())
    return jwt.encode(
        {
            "iss": "shinyhub",
            "sub": "42",
            "aud": slug,
            "preferred_username": username,
            "role": "admin",
            "groups": [],
            "iat": now,
            "exp": now + exp_delta,
        },
        key,
        algorithm="HS256",
    )


class FakeConn:
    def __init__(self, headers):
        self.headers = headers


class FakeSession:
    """Duck-typed stand-in: session_identity must not require Shiny itself."""

    def __init__(self, token=None):
        self.http_conn = FakeConn({} if token is None else {"x-shinyhub-identity-token": token})

    def set_token(self, token):
        self.http_conn.headers = {"x-shinyhub-identity-token": token}


def test_returns_identity_for_a_valid_handshake():
    s = FakeSession(mint())
    assert session_identity(s, key=KEY, slug=SLUG).username == "alice"


def test_anonymous_session_is_none():
    assert session_identity(FakeSession(), key=KEY, slug=SLUG) is None


def test_answer_survives_the_token_expiring():
    s = FakeSession(mint())
    first = session_identity(s, key=KEY, slug=SLUG)

    # The handshake token is now past its exp, which is what happens on any
    # session open longer than 5 minutes.
    s.set_token(mint(exp_delta=-3600))

    # Positive control: verifying the current headers now genuinely fails, so
    # the assertion below is not vacuous.
    with pytest.raises(IdentityError) as caught:
        current_user(s.http_conn.headers, key=KEY, slug=SLUG)
    assert caught.value.reason == "expired"

    assert session_identity(s, key=KEY, slug=SLUG) == first


def test_a_rejected_handshake_keeps_raising_the_same_reason():
    s = FakeSession(mint(key=WRONG_KEY))
    with pytest.raises(IdentityError) as first:
        session_identity(s, key=KEY, slug=SLUG)
    assert first.value.reason == "bad_signature"

    # Even once a good token appears in the headers: the session was opened
    # against a broken deployment and must not silently start working.
    s.set_token(mint())
    with pytest.raises(IdentityError) as second:
        session_identity(s, key=KEY, slug=SLUG)
    assert second.value.reason == "bad_signature"


def test_sessions_do_not_share_an_identity():
    a = FakeSession(mint(username="alice"))
    b = FakeSession(mint(username="bob"))
    assert session_identity(a, key=KEY, slug=SLUG).username == "alice"
    assert session_identity(b, key=KEY, slug=SLUG).username == "bob"
    assert session_identity(a, key=KEY, slug=SLUG).username == "alice"


def test_anonymous_is_cached_too():
    s = FakeSession()
    assert session_identity(s, key=KEY, slug=SLUG) is None
    s.set_token(mint())
    assert session_identity(s, key=KEY, slug=SLUG) is None


def test_non_session_object_is_a_clear_type_error():
    with pytest.raises(TypeError) as caught:
        session_identity(object(), key=KEY, slug=SLUG)
    assert "http_conn" in str(caught.value)


def test_unreferenceable_session_is_refused_not_silently_uncached():
    # Dropping the cache for an exotic session type would silently drop the
    # once-per-session guarantee, so it is an error instead.
    class Slotted:
        __slots__ = ("http_conn",)

        def __init__(self):
            self.http_conn = FakeConn({"x-shinyhub-identity-token": mint()})

    with pytest.raises(TypeError) as caught:
        session_identity(Slotted(), key=KEY, slug=SLUG)
    assert "weak-referenceable" in str(caught.value)


def test_omitting_the_session_outside_shiny_is_a_clear_error():
    # Shiny is not installed in this suite, so the "find the active session"
    # path must say so rather than fail obscurely.
    with pytest.raises(RuntimeError) as caught:
        session_identity(key=KEY, slug=SLUG)
    assert "session" in str(caught.value)
