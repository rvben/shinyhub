"""Selective URL bookmarking for Python Shiny apps hosted by ShinyHub."""

__version__ = "0.1.0"

from ._adapter import Field, Registration, register
from ._dependency import bookmarking_dependency

__all__ = ["Field", "Registration", "bookmarking_dependency", "register"]
