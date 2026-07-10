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
raising, so an app stays testable without SSO. A token that is PRESENT but
rejected additionally logs a WARNING on the "shinyhub_identity" logger (once
per distinct reason per process), because that almost always means a
misconfigured deployment rather than an anonymous visitor.

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
from dataclasses import dataclass
from typing import Any, Mapping, Optional, Union

import jwt  # PyJWT

__all__ = ["Identity", "current_user", "verify_token"]

_ISSUER = "shinyhub"
_TOKEN_HEADER = "x-shinyhub-identity-token"

_log = logging.getLogger("shinyhub_identity")

# Reasons already warned about, so a misconfigured deployment logs each
# distinct problem once per process instead of once per request.
_warned: set = set()


def _warn_once(reason_key: str, detail: str) -> None:
    """Warn that a PRESENT token was rejected. A genuine anonymous visitor
    sends no token at all, so a rejected token almost always means a
    misconfiguration (wrong or missing key, audience/issuer mismatch, clock
    skew) - high-signal, and safe to surface without leaking anything."""
    if reason_key in _warned:
        return
    _warned.add(reason_key)
    _log.warning(
        "identity token present but rejected: %s "
        "(the request is treated as anonymous; a rejected-but-present token "
        "usually means the deployment is misconfigured)",
        detail,
    )


@dataclass(frozen=True)
class Identity:
    """The verified identity of the current request."""

    user_id: str
    username: str
    role: str
    groups: tuple[str, ...]
    groups_truncated: bool
    claims: Mapping[str, Any]
    # Appended with defaults so the positional constructor stays
    # backward-compatible. "" when the upstream IdP asserted no email / name.
    email: str = ""
    name: str = ""


def _resolve_key(
    key: Union[bytes, bytearray, str, None],
) -> "tuple[Optional[bytes], Optional[str]]":
    """Resolve the verification key, returning (key_bytes, problem).

    Exactly one of the two is None: a resolved key carries no problem, and a
    problem string describes why no key is available.
    """
    if key is None:
        key = os.environ.get("SHINYHUB_IDENTITY_KEY")
    if key is None or key == "":
        return None, "no verification key (SHINYHUB_IDENTITY_KEY is unset or empty)"
    if isinstance(key, (bytes, bytearray)):
        return bytes(key), None
    try:
        return bytes.fromhex(key), None
    except ValueError:
        return None, "verification key is not valid hex (check SHINYHUB_IDENTITY_KEY)"


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
        email=claims.get("email", ""),
        name=claims.get("name", ""),
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
    resolved_key, key_problem = _resolve_key(key)
    resolved_slug = _resolve_slug(slug)
    if resolved_key is None:
        _warn_once("no_key", key_problem or "no verification key")
        return None
    if resolved_slug is None:
        _warn_once(
            "no_slug",
            "expected audience unknown (SHINYHUB_APP_SLUG is unset or empty)",
        )
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
    except jwt.InvalidTokenError as exc:
        _warn_once(type(exc).__name__, f"{type(exc).__name__}: {exc}")
        return None
    return _identity_from_claims(claims)


def _dev_identity() -> Optional[Identity]:
    """Synthetic identity for local development, from SHINYHUB_IDENTITY_DEV_*.

    Only active when SHINYHUB_IDENTITY_KEY is absent: ShinyHub always injects
    that key into app processes, so under a real deployment this can never
    substitute for a missing or failed verification.
    """
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
    if "dev_identity" not in _warned:
        _warned.add("dev_identity")
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

    For local development (no ShinyHub proxy, so no token and no injected
    key), setting ``SHINYHUB_IDENTITY_DEV_USER`` (and optionally
    ``..._DEV_GROUPS``/``..._DEV_EMAIL``/``..._DEV_NAME``/``..._DEV_ROLE``)
    makes this return a synthetic Identity with ``claims == {"dev": True, ...}``.
    """
    token = _find_token(headers)
    if not token and key is None:
        dev = _dev_identity()
        if dev is not None:
            return dev
    return verify_token(token, key=key, slug=slug, leeway=leeway)
