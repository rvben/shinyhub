"""ShinyHub — deploy and manage Shiny apps.

This Python package is a distribution vehicle for the ``shinyhub`` CLI.
It does not expose a stable Python API; use the ``shinyhub`` command.
"""

from importlib.metadata import PackageNotFoundError, version as _pkg_version

try:
    __version__ = _pkg_version("shinyhub")
except PackageNotFoundError:
    __version__ = "0.0.0+unknown"

__all__ = ["__version__"]
