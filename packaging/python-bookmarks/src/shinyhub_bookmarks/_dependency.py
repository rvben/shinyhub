from __future__ import annotations

from htmltools import HTMLDependency

from . import __version__


def bookmarking_dependency() -> HTMLDependency:
    """Return the browser bridge dependency for inclusion in an app UI.

    Add this once to the app's UI. It is inert outside ShinyHub and exposes no
    controls of its own; it only connects the app to ShinyHub's injected chrome.
    """

    return HTMLDependency(
        "shinyhub-bookmarks",
        __version__,
        source={"package": "shinyhub_bookmarks", "subdir": "www"},
        script={"src": "bridge.js", "defer": "defer"},
    )

