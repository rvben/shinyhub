"""Mint ShinyHub identity tokens so an app's own tests can exercise its
identity-gated code.

    from shinyhub_identity.shiny import session_identity
    from shinyhub_identity.testing import fake_session, identity_env

    def test_admins_see_the_panel():
        with identity_env():
            session = fake_session(username="alice", groups=["platform-admins"])
            assert session_identity(session).username == "alice"

This module exists because verification is strict by design. ``current_user``
returns ``None`` only for a request that carried no token at all; anything
present but unverifiable raises ``IdentityError``. That is the right behaviour
in production - a broken deployment must never render as an anonymous visitor -
but it means an app author cannot test a signed-in code path by handing over a
made-up string. They need a genuinely valid token, which means matching the
issuer, audience, expiry and signature ShinyHub itself produces.

Everything here is deliberately zero-configuration: ``DEFAULT_KEY`` and
``DEFAULT_SLUG`` are what ``mint_token``, ``fake_session`` and ``identity_env``
all use unless told otherwise, so the three line up with no setup.

**The tokens are real, not stubs.** They carry the same claim set the proxy
mints, including which claims are *omitted* when empty, so an app that reads
``user.claims["email"]`` fails in a test exactly where it would fail in
production. `TestConformance_TestHelperMatchesProduction` in
``internal/identity`` proves that field by field against the production minter,
in both languages, and fails if either drifts.

None of this belongs in production code. The key is a fixed, published
constant, so a token minted here is forgeable by anyone who has read this file;
that is fine for a test and disqualifying anywhere else.
"""

from __future__ import annotations

import os
from collections.abc import Iterator, Mapping, Sequence
from contextlib import contextmanager
from typing import Any, Union

import jwt  # PyJWT

from . import _ISSUER, _TOKEN_HEADER

__all__ = [
    "DEFAULT_KEY",
    "DEFAULT_SLUG",
    "TOKEN_TTL_SECONDS",
    "fake_headers",
    "fake_session",
    "identity_env",
    "mint_token",
]

# A fixed key, so a token minted in one test verifies in another without any
# shared fixture. Deliberately not random: a test that fails only on some runs
# because two helpers disagreed about the key is worse than a forgeable test
# token, and this key protects nothing.
DEFAULT_KEY: bytes = bytes(range(32))

DEFAULT_SLUG = "test-app"

# Mirrors identity.TokenTTL on the server (5 minutes). A test token is not
# shorter-lived than a real one: an app that mishandles a nearly-expired token
# should be able to reproduce that here.
TOKEN_TTL_SECONDS = 300


def _resolve(key: Union[bytes, bytearray, str, None], slug: str | None) -> tuple[bytes, str]:
    resolved_key = DEFAULT_KEY if key is None else key
    if isinstance(resolved_key, str):
        resolved_key = bytes.fromhex(resolved_key)
    return bytes(resolved_key), DEFAULT_SLUG if slug is None else slug


def mint_token(
    *,
    user_id: int | str = 42,
    username: str = "testuser",
    role: str = "viewer",
    app_role: str = "",
    email: str = "",
    name: str = "",
    groups: Sequence[str] = (),
    groups_truncated: bool = False,
    key: Union[bytes, bytearray, str, None] = None,
    slug: str | None = None,
    issuer: str = _ISSUER,
    expires_in: int = TOKEN_TTL_SECONDS,
    issued_at: int | None = None,
) -> str:
    """Mint a valid identity token, shaped exactly as the proxy mints one.

    The defaults produce a token that ``current_user`` accepts with no other
    setup, given ``identity_env()`` or an explicit ``key=``/``slug=``.

    Every rejection path is reachable by changing one argument rather than by a
    separate "make it broken" API, so a test names the thing that is wrong:

    ``expires_in=-60``        -> ``expired``
    ``key=b"..."`` (another)  -> ``bad_signature``
    ``slug="other-app"``      -> ``wrong_audience`` (when verified as this app)
    ``issuer="evil"``         -> ``wrong_issuer``

    A ``malformed`` token needs no helper: pass any non-JWT string as the token.

    ``user_id`` is stamped as ``sub`` in string form, matching the server, which
    formats the numeric user ID. ``groups`` is sorted, because the server sorts
    it before minting and a test asserting on order should see the real one.
    """
    resolved_key, resolved_slug = _resolve(key, slug)
    now = int(_now()) if issued_at is None else int(issued_at)

    # Claim order and omission follow the Go struct tags: role, groups and
    # preferred_username are always present; app_role, email, name and
    # groups_truncated carry omitempty and vanish when empty. An empty group
    # list mints as null, not [], because the server's SanitizeGroups hands
    # MintToken a nil slice.
    claims: dict[str, Any] = {
        "role": role,
        "groups": sorted(groups) if groups else None,
        "preferred_username": username,
        "iss": issuer,
        "sub": str(user_id),
        "aud": [resolved_slug],
        "iat": now,
        "exp": now + expires_in,
    }
    if app_role:
        claims["app_role"] = app_role
    if email:
        claims["email"] = email
    if name:
        claims["name"] = name
    if groups_truncated:
        claims["groups_truncated"] = True

    return jwt.encode(claims, resolved_key, algorithm="HS256")


def _now() -> float:
    import time

    return time.time()


def fake_headers(token: str | None = None, **mint_kwargs: Any) -> dict[str, str]:
    """Header mapping carrying an identity token, for ``current_user``.

    With no arguments this mints a default token; keyword arguments are passed
    to ``mint_token``. Pass ``token=`` to carry a specific string, including a
    deliberately invalid one.

    An anonymous request needs nothing from this module: it is ``{}``.
    """
    if token is None:
        token = mint_token(**mint_kwargs)
    elif mint_kwargs:
        raise TypeError("pass either token= or mint_token arguments, not both")
    return {_TOKEN_HEADER: token}


class _FakeConn:
    __slots__ = ("headers",)

    def __init__(self, headers: Mapping[str, str]) -> None:
        self.headers = headers


class _FakeSession:
    """Minimal stand-in for a Shiny for Python session.

    ``session_identity`` needs two things of a session: ``http_conn.headers``,
    and that it can be weakly referenced so the verified identity is held for
    the session's lifetime. A normal class instance provides both, where a dict
    or SimpleNamespace-with-slots would fail the second in a way whose error
    message points at the wrong problem.
    """

    __slots__ = ("http_conn", "__weakref__")

    def __init__(self, headers: Mapping[str, str]) -> None:
        self.http_conn = _FakeConn(headers)


def fake_session(token: str | None = None, **mint_kwargs: Any) -> Any:
    """A session stub for ``shinyhub_identity.shiny.session_identity``.

    Takes the same arguments as ``fake_headers``. For an anonymous session,
    pass ``token=""``.
    """
    if token == "":
        return _FakeSession({})
    return _FakeSession(fake_headers(token, **mint_kwargs))


@contextmanager
def identity_env(
    key: Union[bytes, bytearray, str, None] = None,
    slug: str | None = None,
) -> Iterator[tuple[bytes, str]]:
    """Set the environment ShinyHub injects, and restore it on exit.

    An app calls ``current_user(headers)`` or ``session_identity(session)``
    without passing a key, reading ``SHINYHUB_IDENTITY_KEY`` and
    ``SHINYHUB_APP_SLUG`` from its environment. To test that call as the app
    actually writes it, those variables have to be set; this sets them to the
    same defaults the minting helpers use.

    Yields ``(key, slug)`` for a test that needs to mint against them
    explicitly. Prior values are restored, including their absence.
    """
    resolved_key, resolved_slug = _resolve(key, slug)
    previous = {
        "SHINYHUB_IDENTITY_KEY": os.environ.get("SHINYHUB_IDENTITY_KEY"),
        "SHINYHUB_APP_SLUG": os.environ.get("SHINYHUB_APP_SLUG"),
    }
    os.environ["SHINYHUB_IDENTITY_KEY"] = resolved_key.hex()
    os.environ["SHINYHUB_APP_SLUG"] = resolved_slug
    try:
        yield resolved_key, resolved_slug
    finally:
        for name, value in previous.items():
            if value is None:
                os.environ.pop(name, None)
            else:
                os.environ[name] = value
