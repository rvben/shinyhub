"""Cost-free entrypoint for the public agent capabilities demo."""

import os

for name in ("OPENAI_API_KEY", "AGENT_DEMO_AGUI_URL", "AGENT_DEMO_AGUI_TOKEN"):
    if os.environ.get(name):
        raise RuntimeError(f"Public agent demo refuses {name}")

os.environ["AGENT_DEMO_SCRIPTED_ONLY"] = "true"

from app import app  # noqa: E402
