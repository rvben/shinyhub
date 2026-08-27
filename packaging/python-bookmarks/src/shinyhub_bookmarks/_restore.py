from __future__ import annotations

from collections.abc import Callable, Iterable, Mapping, Sequence
from dataclasses import dataclass, field
from typing import Any, Literal


class _Missing:
    pass


MISSING = _Missing()
ChoiceControl = Literal["select", "selectize", "radio"]
ChoiceSource = (
    Mapping[Any, Any] | Iterable[Any] | Callable[[], Mapping[Any, Any] | Iterable[Any]]
)


@dataclass(frozen=True, slots=True)
class ChoiceRestore:
    """Rules for restoring a choice-based Shiny input safely.

    ``choices`` may be a collection, a Shiny-style value-to-label mapping, or a
    zero-argument callable for choices that change while the app is running.
    ``aliases`` maps retired values to their current equivalent. When a saved
    value is no longer available, ``default`` wins; without one, the value
    already selected by Shiny is retained.
    """

    choices: ChoiceSource
    default: Any = MISSING
    aliases: Mapping[Any, Any] = field(default_factory=dict)
    control: ChoiceControl = "select"

    def __post_init__(self) -> None:
        if self.control not in {"select", "selectize", "radio"}:
            raise ValueError(
                "ChoiceRestore control must be select, selectize, or radio"
            )
        if not isinstance(self.aliases, Mapping):
            raise TypeError("ChoiceRestore aliases must be a mapping")


@dataclass(frozen=True, slots=True)
class Adjustment:
    """A safe, human-readable account of one restored bookmark change."""

    kind: Literal[
        "migrated", "fallback", "renamed", "removed", "unknown", "unknown_summary"
    ]
    label: str
    previous: str = ""
    current: str = ""
    source_label: str = ""

    def as_message(self) -> dict[str, str]:
        message = {
            "kind": self.kind,
            "label": self.label,
            "previous": self.previous,
            "current": self.current,
        }
        if self.source_label:
            message["sourceLabel"] = self.source_label
        return message


@dataclass(frozen=True, slots=True)
class ChoiceResolution:
    value: Any
    kind: Literal["migrated", "fallback"] | None


def values_equal(left: Any, right: Any) -> bool:
    """Compare restored values while treating list/tuple transport equally."""

    if isinstance(left, Sequence) and not isinstance(left, (str, bytes)):
        if not isinstance(right, Sequence) or isinstance(right, (str, bytes)):
            return False
        return list(left) == list(right)
    try:
        return bool(left == right)
    except Exception:
        return False


def _mapping_value(mapping: Mapping[Any, Any], value: Any) -> tuple[Any, bool]:
    for old, new in mapping.items():
        if values_equal(old, value):
            return new, True
    return value, False


def _choice_values(source: ChoiceSource) -> list[Any]:
    choices = source() if callable(source) else source
    if isinstance(choices, Mapping):
        values: list[Any] = []
        for key, label in choices.items():
            if isinstance(label, Mapping):
                values.extend(_choice_values(label))
            else:
                values.append(key)
        return values
    if isinstance(choices, (str, bytes)):
        return [choices]
    return list(choices)


def _contains(choices: Sequence[Any], value: Any) -> bool:
    return any(values_equal(choice, value) for choice in choices)


def _valid_multiple(choices: Sequence[Any], value: Any) -> bool:
    return (
        isinstance(value, Sequence)
        and not isinstance(value, (str, bytes))
        and all(_contains(choices, item) for item in value)
    )


def resolve_choice(policy: ChoiceRestore, saved: Any, current: Any) -> ChoiceResolution:
    """Resolve a saved choice to a value the current app can represent."""

    choices = _choice_values(policy.choices)
    multiple = isinstance(saved, Sequence) and not isinstance(saved, (str, bytes))

    if multiple:
        migrated: list[Any] = []
        used_alias = False
        lost_value = False
        for item in saved:
            candidate, changed = _mapping_value(policy.aliases, item)
            used_alias = used_alias or changed
            if _contains(choices, candidate):
                migrated.append(candidate)
            else:
                lost_value = True
        if not lost_value:
            return ChoiceResolution(migrated, "migrated" if used_alias else None)
        if migrated:
            return ChoiceResolution(migrated, "fallback")
        if not isinstance(policy.default, _Missing) and _valid_multiple(
            choices, policy.default
        ):
            return ChoiceResolution(list(policy.default), "fallback")
        if _valid_multiple(choices, current):
            return ChoiceResolution(list(current), "fallback")
        return ChoiceResolution([], "fallback")

    candidate, used_alias = _mapping_value(policy.aliases, saved)
    if _contains(choices, candidate):
        return ChoiceResolution(candidate, "migrated" if used_alias else None)
    if not isinstance(policy.default, _Missing) and _contains(choices, policy.default):
        return ChoiceResolution(policy.default, "fallback")
    if _contains(choices, current):
        return ChoiceResolution(current, "fallback")
    fallback = choices[0] if choices else current
    return ChoiceResolution(fallback, "fallback")
