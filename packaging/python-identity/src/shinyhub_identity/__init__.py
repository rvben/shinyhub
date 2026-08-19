"""Read the signed identity ShinyHub forwards to an app, in one call.

ShinyHub injects a short-lived, per-app HS256 JWT (``X-Shinyhub-Identity-Token``)
into every proxied request, and hands the app its verification key via the
``SHINYHUB_IDENTITY_KEY`` (hex) and ``SHINYHUB_APP_SLUG`` environment variables.
This module verifies that token and returns the identity, so apps need no JWT
plumbing of their own.

    from shinyhub_identity import current_user

    def server(input, output, session):
        user = current_user(session.http_conn.headers)   # None when anonymous
        if user and "platform-admins" in user.groups:
            ...

``None`` means exactly one thing: the request carried no identity token, so the
visitor is anonymous. A token that IS present but fails verification raises
``IdentityError`` instead, because that is a broken deployment (wrong or missing
key, audience/issuer mismatch, clock skew) and rendering it as "anonymous" hides
the outage behind an empty dashboard. ``IdentityError.reason`` classifies it.

In a Shiny for Python app, prefer ``shinyhub_identity.shiny.session_identity``:
it takes the session instead of its headers, and verifies once per session
rather than on every reactive read.

For local development without a ShinyHub proxy, set
``SHINYHUB_IDENTITY_DEV_USER`` (and optionally ``..._DEV_GROUPS`` /
``..._DEV_EMAIL`` / ``..._DEV_NAME`` / ``..._DEV_ROLE``) to make
``current_user`` return a synthetic Identity marked ``claims={"dev": True}``.
It never activates when ``SHINYHUB_IDENTITY_KEY`` is set, so it cannot mask a
real verification failure in a deployment.
"""

from __future__ import annotations

import logging
import os
from collections.abc import Mapping
from dataclasses import dataclass, field
from typing import Any, Union

import jwt  # PyJWT

__all__ = ["Identity", "IdentityError", "current_user", "verify_token"]

_ISSUER = "shinyhub"
_TOKEN_HEADER = "x-shinyhub-identity-token"

_log = logging.getLogger("shinyhub_identity")


class IdentityError(Exception):
    """An identity token was present but could not be verified.

    Raised rather than returned as ``None`` so a misconfigured deployment can
    never masquerade as an anonymous visitor: a genuine anonymous visitor sends
    no token at all.

    ``reason`` is a stable, machine-readable classification, and is the same
    vocabulary the R helper uses:

    ``no_token``        no token was passed to ``verify_token``
    ``no_key``          ``SHINYHUB_IDENTITY_KEY`` is unset or empty
    ``bad_key``         ``SHINYHUB_IDENTITY_KEY`` is not valid hex
    ``no_slug``         ``SHINYHUB_APP_SLUG`` is unset or empty
    ``bad_signature``   signed with a different key
    ``expired``         past its ``exp`` (tokens live 5 minutes)
    ``wrong_audience``  minted for a different app slug
    ``wrong_issuer``    ``iss`` is not ``shinyhub``
    ``malformed``       unparseable, or missing the required ``exp`` claim

    ``detail`` carries the human-readable specifics for a log line. Nothing in
    either field is attacker-controlled beyond the token's own parse errors.
    """

    def __init__(self, reason: str, detail: str) -> None:
        super().__init__(f"identity token rejected ({reason}): {detail}")
        self.reason = reason
        self.detail = detail


@dataclass(frozen=True, kw_only=True)
class Identity:
    """The verified identity of the current request.

    Keyword-only, so adding a field never depends on where it sits in the
    signature.
    """

    user_id: str
    username: str
    role: str
    groups: tuple[str, ...] = ()
    # "" when the upstream IdP asserted no email / name, which is the normal
    # case for local username/password accounts.
    name: str = ""
    email: str = ""
    groups_truncated: bool = False
    claims: Mapping[str, Any] = field(default_factory=dict)


def _resolve_key(key: Union[bytes, bytearray, str, None]) -> bytes:
    """Resolve the verification key, raising IdentityError when unavailable."""
    if key is None:
        key = os.environ.get("SHINYHUB_IDENTITY_KEY")
    if not key:
        raise IdentityError(
            "no_key", "no verification key (SHINYHUB_IDENTITY_KEY is unset or empty)"
        )
    if isinstance(key, (bytes, bytearray)):
        return bytes(key)
    try:
        return bytes.fromhex(key)
    except ValueError:
        raise IdentityError(
            "bad_key", "verification key is not valid hex (check SHINYHUB_IDENTITY_KEY)"
        ) from None


def _resolve_slug(slug: str | None) -> str:
    if slug is None:
        slug = os.environ.get("SHINYHUB_APP_SLUG")
    if not slug:
        raise IdentityError(
            "no_slug", "expected audience unknown (SHINYHUB_APP_SLUG is unset or empty)"
        )
    return slug


# Most specific PyJWT class first: InvalidSignatureError subclasses DecodeError,
# and every entry here subclasses InvalidTokenError.
_JWT_REASONS: tuple[tuple[type[Exception], str], ...] = (
    (jwt.ExpiredSignatureError, "expired"),
    (jwt.InvalidAudienceError, "wrong_audience"),
    (jwt.InvalidIssuerError, "wrong_issuer"),
    (jwt.InvalidSignatureError, "bad_signature"),
)


def _reason_for(exc: Exception) -> str:
    for cls, reason in _JWT_REASONS:
        if isinstance(exc, cls):
            return reason
    return "malformed"


def _identity_from_claims(claims: Mapping[str, Any]) -> Identity:
    groups = claims.get("groups") or []
    return Identity(
        user_id=str(claims.get("sub", "")),
        username=claims.get("preferred_username", ""),
        role=claims.get("role", ""),
        email=claims.get("email", ""),
        name=claims.get("name", ""),
        groups=tuple(groups),
        groups_truncated=bool(claims.get("groups_truncated", False)),
        claims=claims,
    )


def verify_token(
    token: str | None,
    *,
    key: Union[bytes, bytearray, str, None] = None,
    slug: str | None = None,
    leeway: int = 30,
) -> Identity:
    """Verify a raw identity token and return the Identity.

    ``key`` and ``slug`` default to ``SHINYHUB_IDENTITY_KEY`` (hex) and
    ``SHINYHUB_APP_SLUG`` from the environment.

    Every failure raises ``IdentityError``, including an empty token (reason
    ``no_token``). This primitive is for code that already knows a token is
    expected; use ``current_user`` when an absent token means "anonymous".
    """
    if not token:
        raise IdentityError("no_token", "no identity token to verify")
    resolved_key = _resolve_key(key)
    resolved_slug = _resolve_slug(slug)
    try:
        claims = jwt.decode(
            token,
            resolved_key,
            algorithms=["HS256"],
            audience=resolved_slug,
            issuer=_ISSUER,
            leeway=leeway,
            # exp is only *validated* when present; require it so a token that
            # omits it cannot bypass the short-lived-token / replay bound.
            options={"require": ["exp"]},
        )
    except jwt.InvalidTokenError as exc:
        raise IdentityError(_reason_for(exc), f"{type(exc).__name__}: {exc}") from exc
    return _identity_from_claims(claims)


_dev_logged = False


def _dev_identity() -> Identity | None:
    """Synthetic identity for local development, from SHINYHUB_IDENTITY_DEV_*.

    Only active when SHINYHUB_IDENTITY_KEY is absent: ShinyHub always injects
    that key into app processes, so under a real deployment this can never
    substitute for a missing or failed verification.
    """
    global _dev_logged
    username = os.environ.get("SHINYHUB_IDENTITY_DEV_USER")
    if not username or os.environ.get("SHINYHUB_IDENTITY_KEY"):
        return None
    groups = tuple(
        g.strip()
        for g in os.environ.get("SHINYHUB_IDENTITY_DEV_GROUPS", "").split(",")
        if g.strip()
    )
    role = os.environ.get("SHINYHUB_IDENTITY_DEV_ROLE", "viewer")
    email = os.environ.get("SHINYHUB_IDENTITY_DEV_EMAIL", "")
    name = os.environ.get("SHINYHUB_IDENTITY_DEV_NAME", "")
    claims: Mapping[str, Any] = {
        "dev": True,
        "sub": username,
        "preferred_username": username,
        "role": role,
        "email": email,
        "name": name,
        "groups": list(groups),
    }
    if not _dev_logged:
        _dev_logged = True
        _log.info(
            "returning dev identity %r from SHINYHUB_IDENTITY_DEV_USER "
            "(local development only; inactive whenever SHINYHUB_IDENTITY_KEY "
            "is set)",
            username,
        )
    return Identity(
        user_id=username,
        username=username,
        role=role,
        email=email,
        name=name,
        groups=groups,
        groups_truncated=False,
        claims=claims,
    )


def _find_token(headers: Any) -> str | None:
    # Frameworks vary: Starlette Headers.get is case-insensitive; a plain dict
    # may hold the header in canonical casing. Try the direct get, then scan.
    try:
        token = headers.get(_TOKEN_HEADER)
        if token:
            return token
    except (AttributeError, TypeError):
        pass
    try:
        items = headers.items()
    except (AttributeError, TypeError):
        return None
    for name, value in items:
        if isinstance(name, str) and name.lower() == _TOKEN_HEADER:
            return value
    return None


def current_user(
    headers: Any,
    *,
    key: Union[bytes, bytearray, str, None] = None,
    slug: str | None = None,
    leeway: int = 30,
) -> Identity | None:
    """Return the verified identity for a request, or None when anonymous.

    ``headers`` is any header mapping (e.g. a Shiny for Python
    ``session.http_conn.headers``, a Starlette/Flask request's headers, or a
    plain dict). ``key``/``slug`` default to the ShinyHub-injected environment.

    ``None`` means the request carried no identity token. A token that is
    present but fails verification raises ``IdentityError``: that is a
    misconfigured deployment, not a visitor, and the two must not look alike.

    For local development (no ShinyHub proxy, so no token and no injected
    key), setting ``SHINYHUB_IDENTITY_DEV_USER`` (and optionally
    ``..._DEV_GROUPS``/``..._DEV_EMAIL``/``..._DEV_NAME``/``..._DEV_ROLE``)
    makes this return a synthetic Identity with ``claims == {"dev": True, ...}``.
    """
    token = _find_token(headers)
    if not token:
        if key is None:
            dev = _dev_identity()
            if dev is not None:
                return dev
        return None
    return verify_token(token, key=key, slug=slug, leeway=leeway)
