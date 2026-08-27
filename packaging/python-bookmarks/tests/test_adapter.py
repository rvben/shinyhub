from __future__ import annotations

from dataclasses import dataclass

import pytest

from shinyhub_bookmarks._adapter import (
    Field,
    _create_url,
    _display_value,
    _normalise_fields,
    _selection_from_request,
)


class Inputs:
    def __dir__(self):
        return ["region", "period", "clientdata_url_hostname", ".private"]


@dataclass
class Bookmark:
    exclude: list[str]
    url: str = "https://example.test/app?_inputs_=saved"

    async def get_bookmark_url(self):
        return self.url


def test_normalise_fields_accepts_labels_and_resolves_ids():
    fields = _normalise_fields(
        {"region": "Region", "period": Field("Reporting period")},
        resolve=lambda value: f"module-{value}",
    )
    assert [(field.input_id, field.resolved_id, field.label) for field in fields] == [
        ("region", "module-region", "Region"),
        ("period", "module-period", "Reporting period"),
    ]


@pytest.mark.parametrize(
    ("value", "expected"),
    [(None, "Not set"), (True, "On"), ([], "None"), (["A", "B"], "A, B"), ({"a": 1}, "1 selected")],
)
def test_display_value_is_human_readable(value, expected):
    assert _display_value(value, None) == expected


def test_selection_rejects_unknown_and_empty_fields():
    with pytest.raises(ValueError, match="at least one"):
        _selection_from_request({"version": 1, "requestId": "r1", "include": []}, {"region"})
    with pytest.raises(ValueError, match="unknown"):
        _selection_from_request({"version": 1, "requestId": "r1", "include": ["secret"]}, {"region"})


@pytest.mark.asyncio
async def test_create_url_excludes_unselected_inputs_and_restores_author_settings():
    bookmark = Bookmark(exclude=["author_excluded"])
    # The fake asserts the effective exclusion while serialising. The original
    # app-authored list is put back even after the await.
    async def get_url():
        assert bookmark.exclude == [
            ".shinyhub_bookmark_discover",
            ".shinyhub_bookmark_request",
            "author_excluded",
            "period",
        ]
        return bookmark.url
    bookmark.get_bookmark_url = get_url

    result = await _create_url(
        bookmark=bookmark,
        inputs=Inputs(),
        selected=["region"],
        max_url_length=8192,
    )

    assert result == bookmark.url
    assert bookmark.exclude == ["author_excluded"]


@pytest.mark.asyncio
async def test_create_url_restores_exclusions_after_failure():
    bookmark = Bookmark(exclude=["keep"])
    async def fail():
        raise RuntimeError("boom")
    bookmark.get_bookmark_url = fail

    with pytest.raises(RuntimeError, match="boom"):
        await _create_url(bookmark=bookmark, inputs=Inputs(), selected=["region"], max_url_length=8192)

    assert bookmark.exclude == ["keep"]


@pytest.mark.asyncio
async def test_selected_registration_temporarily_wins_over_an_author_exclusion():
    bookmark = Bookmark(exclude=["region"])
    async def get_url():
        assert bookmark.exclude == [
            ".shinyhub_bookmark_discover",
            ".shinyhub_bookmark_request",
            "period",
        ]
        return bookmark.url
    bookmark.get_bookmark_url = get_url

    await _create_url(bookmark=bookmark, inputs=Inputs(), selected=["region"], max_url_length=8192)

    assert bookmark.exclude == ["region"]
