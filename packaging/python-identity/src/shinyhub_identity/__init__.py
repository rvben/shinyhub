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

Every failure mode - no token, bad signature, wrong audience/issuer, expired,
or no ShinyHub in front at all (running locally) - returns ``None`` rather than
raising, so an app stays testable without SSO.
"""

from __future__ import annotations

import os
from dataclasses import dataclass
from typing import Any, Mapping, Optional, Union

import jwt  # PyJWT

__all__ = ["Identity", "current_user", "verify_token"]

_ISSUER = "shinyhub"
_TOKEN_HEADER = "x-shinyhub-identity-token"


@dataclass(frozen=True)
class Identity:
    """The verified identity of the current request."""

    user_id: str
    username: str
    role: str
    groups: tuple[str, ...]
    groups_truncated: bool
    claims: Mapping[str, Any]


def _resolve_key(key: Union[bytes, bytearray, str, None]) -> Optional[bytes]:
    if key is None:
        key = os.environ.get("SHINYHUB_IDENTITY_KEY")
    if key is None or key == "":
        return None
    if isinstance(key, (bytes, bytearray)):
        return bytes(key)
    try:
        return bytes.fromhex(key)
    except ValueError:
        return None


def _resolve_slug(slug: Optional[str]) -> Optional[str]:
    if slug is None:
        slug = os.environ.get("SHINYHUB_APP_SLUG")
    return slug or None


def _identity_from_claims(claims: Mapping[str, Any]) -> Identity:
    groups = claims.get("groups") or []
    return Identity(
        user_id=str(claims.get("sub", "")),
        username=claims.get("preferred_username", ""),
        role=claims.get("role", ""),
        groups=tuple(groups),
        groups_truncated=bool(claims.get("groups_truncated", False)),
        claims=claims,
    )


def verify_token(
    token: Optional[str],
    *,
    key: Union[bytes, bytearray, str, None] = None,
    slug: Optional[str] = None,
    leeway: int = 30,
) -> Optional[Identity]:
    """Verify a raw identity token, returning the Identity or None.

    ``key`` and ``slug`` default to ``SHINYHUB_IDENTITY_KEY`` (hex) and
    ``SHINYHUB_APP_SLUG`` from the environment. A missing token, missing key,
    or any verification failure returns None.
    """
    if not token:
        return None
    resolved_key = _resolve_key(key)
    resolved_slug = _resolve_slug(slug)
    if resolved_key is None or resolved_slug is None:
        return None
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
    except jwt.InvalidTokenError:
        return None
    return _identity_from_claims(claims)


def _find_token(headers: Any) -> Optional[str]:
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
    slug: Optional[str] = None,
    leeway: int = 30,
) -> Optional[Identity]:
    """Return the verified identity for a request, or None when anonymous.

    ``headers`` is any header mapping (e.g. a Shiny for Python
    ``session.http_conn.headers``, a Starlette/Flask request's headers, or a
    plain dict). ``key``/``slug`` default to the ShinyHub-injected environment.
    """
    return verify_token(_find_token(headers), key=key, slug=slug, leeway=leeway)
