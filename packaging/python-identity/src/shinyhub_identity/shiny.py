"""Shiny-native identity: take the session, verify once for its lifetime.

    from shinyhub_identity.shiny import session_identity

    def server(input, output, session):
        user = session_identity(session)       # None when anonymous

This exists for two reasons, one ergonomic and one correctness.

Ergonomic: the app hands over the ``session`` it already has, instead of
reaching through it for ``session.http_conn.headers``. The R helper has always
taken the session; this makes the two SDKs symmetric.

Correctness: ShinyHub binds identity at the WebSocket upgrade, and the token it
forwarded on that handshake expires five minutes later. Re-reading the
handshake headers from a reactive therefore starts failing part-way through a
long session even though nothing about the user changed - the same token, now
past its ``exp``. ``session_identity`` verifies once, on first call, and returns
that answer (or re-raises that ``IdentityError``) for as long as the session
lives, which is the window ShinyHub's own access check governs.

Nothing here imports Shiny at module load: any object exposing
``http_conn.headers`` works, so an app's tests can pass a stub. Shiny is only
imported when you omit ``session`` and ask it to find the active one.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any, Union
from weakref import WeakKeyDictionary

from . import Identity, IdentityError, current_user

if TYPE_CHECKING:  # pragma: no cover - typing only
    from shiny import Session

__all__ = ["session_identity"]

# Keyed weakly so a finished session's cached identity is collected with it.
# Value is (identity, error): exactly one is None.
_cache: WeakKeyDictionary = WeakKeyDictionary()


def _active_session() -> Any:
    try:
        from shiny.session import get_current_session
    except ImportError as exc:  # pragma: no cover - exercised only without Shiny
        raise RuntimeError(
            "shinyhub_identity.shiny needs Shiny for Python installed to find the "
            "active session; pass session= explicitly instead"
        ) from exc
    session = get_current_session()
    if session is None:
        raise RuntimeError(
            "no active Shiny session (called outside a session context); "
            "pass session= explicitly"
        )
    return session


def _headers_of(session: Any) -> Any:
    conn = getattr(session, "http_conn", None)
    headers = getattr(conn, "headers", None)
    if headers is None:
        raise TypeError(
            "session has no http_conn.headers; expected a Shiny for Python "
            f"session, got {type(session).__name__}"
        )
    return headers


def _cached(session: Any) -> Any:
    """Cached outcome for this session, or None when not verified yet.

    A session that cannot be weakly referenced is refused rather than verified
    uncached: silently dropping the cache would silently drop the once-per-
    session guarantee this function exists to provide.
    """
    try:
        return _cache.get(session)
    except TypeError as exc:
        raise TypeError(
            "session must be weak-referenceable so its identity can be held "
            f"for the session's lifetime; got {type(session).__name__}"
        ) from exc


def session_identity(
    session: Session | None = None,
    *,
    key: Union[bytes, bytearray, str, None] = None,
    slug: str | None = None,
    leeway: int = 30,
) -> Identity | None:
    """Verified identity of this Shiny session, or None when anonymous.

    ``session`` defaults to the session Shiny considers active, so this can be
    called from inside a reactive without threading the session through.

    Returns the same answer every time it is called for a given session: the
    handshake token is verified on the first call only, so ``key``, ``slug``
    and ``leeway`` are read on the first call only too. A token that was
    present but failed verification raises ``IdentityError`` on every call,
    with the same ``reason``, rather than degrading into ``None``.
    """
    if session is None:
        session = _active_session()
    # Shape-check before the cache lookup: the likeliest mistake is passing
    # something that is not a session at all, and "no http_conn.headers" names
    # that directly, where the weak-reference refusal below would not.
    headers = _headers_of(session)
    outcome = _cached(session)
    if outcome is None:
        try:
            outcome = (current_user(headers, key=key, slug=slug, leeway=leeway), None)
        except IdentityError as exc:
            outcome = (None, (exc.reason, exc.detail))
        _cache[session] = outcome
    identity, error = outcome
    if error is not None:
        raise IdentityError(*error)
    return identity
