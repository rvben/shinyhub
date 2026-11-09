"""Shared generators and real root-request rendering for bookmark fuzzing."""

from html.parser import HTMLParser
from urllib.parse import urlencode

from hypothesis import strategies as st
from shiny import App, ui
from shiny.bookmark._serializers import serializer_default
from shiny.bookmark._utils import to_json_str
from starlette.requests import Request

text = st.text(st.characters(exclude_categories=("Cs", "Cc")), max_size=24)
json_text = st.text(st.characters(exclude_categories=("Cs",)), max_size=24)
json_values = st.recursive(
    st.none()
    | st.booleans()
    | st.integers(min_value=-(2**53), max_value=2**53)
    | st.floats(allow_nan=False, allow_infinity=False)
    | json_text,
    lambda children: st.lists(children, max_size=5)
    | st.dictionaries(json_text, children, max_size=5),
    max_leaves=20,
)


class Markup(HTMLParser):
    def __init__(self, html):
        super().__init__(convert_charrefs=True)
        self.sliders = []
        self.options = []
        self.labels = []
        self.groups = []
        self.select_id = None
        self.sibling = None
        self.in_option = False
        self.feed(html)

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "input" and attrs.get("class") == "js-range-slider":
            self.sliders.append(attrs)
        if tag == "option":
            self.options.append(attrs)
            self.labels.append("")
            self.in_option = True
        elif tag == "optgroup":
            self.groups.append(attrs["label"])
        elif tag == "select":
            self.select_id = attrs["id"]
        elif tag == "input" and attrs.get("id") == "sibling":
            self.sibling = attrs.get("value")

    def handle_endtag(self, tag):
        if tag == "option":
            self.in_option = False

    def handle_data(self, data):
        if self.in_option:
            self.labels[-1] += data


async def root_get(control, saved, input_id="x", raw_query=None):
    app = App(
        lambda request: ui.page_fluid(
            control(), ui.input_text("sibling", "Sibling", value="default")
        ),
        server=None,
        bookmark_store="url",
    )
    query = raw_query
    if query is None:
        serialized = await serializer_default(saved, None)
        query = "_inputs_&" + urlencode(
            {input_id: to_json_str(serialized), "sibling": to_json_str("restored")}
        )
    request = Request(
        dict(
            type="http",
            method="GET",
            path="/",
            scheme="http",
            server=("test", 80),
            headers=[],
            query_string=query.encode(),
        )
    )
    response = await app._on_root_request_cb(request)
    assert response.status_code == 200
    return bytes(response.body).decode()
