from __future__ import annotations

from collections.abc import Callable, Iterable, Mapping, Sequence
from dataclasses import dataclass, field, fields, is_dataclass
from datetime import date, time
from enum import Enum
from typing import Any, Literal
from uuid import UUID


class _Missing:
    pass


MISSING = _Missing()
ChoiceControl = Literal["select", "selectize", "radio"]
ChoiceSource = (
    Mapping[Any, Any] | Iterable[Any] | Callable[[], Mapping[Any, Any] | Iterable[Any]]
)
ValueEqual = Callable[[Any, Any], bool]


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
        if isinstance(self.choices, (set, frozenset)):
            raise TypeError("ChoiceRestore choices must preserve display order")


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


def _comparable(value: Any) -> Any:
    if isinstance(value, Enum):
        return _comparable(value.value)
    if isinstance(value, (date, time)):
        return value.isoformat()
    if isinstance(value, UUID):
        return str(value)
    if is_dataclass(value) and not isinstance(value, type):
        return {item.name: getattr(value, item.name) for item in fields(value)}
    return value


def _values_equal(left: Any, right: Any) -> bool:
    left = _comparable(left)
    right = _comparable(right)

    if isinstance(left, bool) or isinstance(right, bool):
        return type(left) is type(right) and left == right

    left_mapping = isinstance(left, Mapping)
    right_mapping = isinstance(right, Mapping)
    if left_mapping or right_mapping:
        if not left_mapping or not right_mapping or len(left) != len(right):
            return False
        unmatched = list(right.items())
        for left_key, left_value in left.items():
            for index, (right_key, right_value) in enumerate(unmatched):
                if _values_equal(left_key, right_key) and _values_equal(
                    left_value, right_value
                ):
                    unmatched.pop(index)
                    break
            else:
                return False
        return True

    left_sequence = isinstance(left, Sequence) and not isinstance(left, (str, bytes))
    right_sequence = isinstance(right, Sequence) and not isinstance(
        right, (str, bytes)
    )
    if left_sequence or right_sequence:
        return (
            left_sequence
            and right_sequence
            and len(left) == len(right)
            and all(
                _values_equal(left_value, right_value)
                for left_value, right_value in zip(left, right)
            )
        )

    return bool(left == right)


def values_equal(left: Any, right: Any) -> bool:
    """Compare values across Shiny's JSON and live-input representations."""

    try:
        return _values_equal(left, right)
    except Exception:
        return False


def _mapping_value(
    mapping: Mapping[Any, Any], value: Any, equal: ValueEqual
) -> tuple[Any, bool]:
    for old, new in mapping.items():
        if equal(old, value):
            return new, True
    return value, False


def _choice_values(source: ChoiceSource) -> list[Any]:
    choices = source() if callable(source) else source
    if isinstance(choices, (set, frozenset)):
        raise TypeError("ChoiceRestore choices must preserve display order")
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


def _contains(choices: Sequence[Any], value: Any, equal: ValueEqual) -> bool:
    return any(equal(choice, value) for choice in choices)


def _valid_multiple(
    choices: Sequence[Any], value: Any, equal: ValueEqual
) -> bool:
    return (
        isinstance(value, Sequence)
        and not isinstance(value, (str, bytes))
        and all(_contains(choices, item, equal) for item in value)
    )


def _canonical_multiple(
    choices: Sequence[Any], selected: Sequence[Any], equal: ValueEqual
) -> list[Any]:
    return [
        choice
        for choice in choices
        if any(equal(choice, item) for item in selected)
    ]


def resolve_choice(
    policy: ChoiceRestore,
    saved: Any,
    current: Any,
    *,
    equal: ValueEqual = values_equal,
) -> ChoiceResolution:
    """Resolve a saved choice to a value the current app can represent."""

    choices = _choice_values(policy.choices)
    if saved is None and current is None:
        return ChoiceResolution(None, None)
    multiple = isinstance(saved, Sequence) and not isinstance(saved, (str, bytes))

    if multiple:
        migrated: list[Any] = []
        used_alias = False
        lost_value = False
        for item in saved:
            candidate, changed = _mapping_value(policy.aliases, item, equal)
            used_alias = used_alias or changed
            if _contains(choices, candidate, equal):
                migrated.append(candidate)
            else:
                lost_value = True
        migrated = _canonical_multiple(choices, migrated, equal)
        if not lost_value:
            return ChoiceResolution(migrated, "migrated" if used_alias else None)
        if migrated:
            return ChoiceResolution(migrated, "fallback")
        if not isinstance(policy.default, _Missing) and _valid_multiple(
            choices, policy.default, equal
        ):
            return ChoiceResolution(
                _canonical_multiple(choices, policy.default, equal), "fallback"
            )
        if _valid_multiple(choices, current, equal):
            return ChoiceResolution(
                _canonical_multiple(choices, current, equal), "fallback"
            )
        return ChoiceResolution([], "fallback")

    candidate, used_alias = _mapping_value(policy.aliases, saved, equal)
    if _contains(choices, candidate, equal):
        return ChoiceResolution(candidate, "migrated" if used_alias else None)
    if not isinstance(policy.default, _Missing) and _contains(
        choices, policy.default, equal
    ):
        return ChoiceResolution(policy.default, "fallback")
    if _contains(choices, current, equal):
        return ChoiceResolution(current, "fallback")
    fallback = choices[0] if choices else current
    return ChoiceResolution(fallback, "fallback")
