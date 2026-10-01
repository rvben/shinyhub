"""The built-in panel's formatting contract, shared by model adapters."""

ANSWER_FORMAT = (
    "Answers are shown with basic Markdown: paragraphs and line breaks, "
    "**bold**, *italic*, inline code, bullet and numbered lists with one nested level, "
    "fenced code blocks, and simple pipe tables with a header and separator row. "
    "Use ---: in table separators to right-align numeric columns. "
    "Headings appear as bold paragraphs. Links, images, HTML, blockquotes, "
    "footnotes and task lists are shown as literal text. "
    "Follow the app's explicit formatting preferences when provided."
)


def panel_instructions(instructions: str) -> str:
    return instructions + "\n\n" + ANSWER_FORMAT
