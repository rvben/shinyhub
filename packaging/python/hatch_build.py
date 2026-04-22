"""Hatchling hooks for the shinyhub wheel.

CI exports two env vars before invoking ``python -m build``:

* ``SHINYHUB_WHEEL_VERSION`` — the Git tag without the leading ``v``
  (e.g. ``0.3.0``). ``VersionHook`` writes it into the project metadata.
* ``SHINYHUB_WHEEL_PLATFORM`` — the wheel platform tag for this matrix
  entry (e.g. ``manylinux_2_17_x86_64``). ``PlatformTagHook`` writes it
  into the wheel's build-data.

Local sanity builds without either env var get sensible defaults so the
build does not blow up; those wheels are only ever used for smoke tests.

When pip falls back to building a wheel from the sdist (because no
matching binary wheel exists for the user's platform), ``PlatformTagHook``
raises a ``RuntimeError`` with a clear explanation — see the
binary-existence check in ``initialize``.
"""
import os

from hatchling.builders.hooks.plugin.interface import BuildHookInterface
from hatchling.metadata.plugin.interface import MetadataHookInterface


UNSUPPORTED_PLATFORM_MESSAGE = """\
shinyhub does not ship a prebuilt binary for your platform.
Supported: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64.

For other platforms:
  - Use the Docker image: ghcr.io/rvben/shinyhub:latest
  - Or build from source: https://github.com/rvben/shinyhub#from-source
"""


class VersionHook(MetadataHookInterface):
    """Sources the package version from SHINYHUB_WHEEL_VERSION."""

    PLUGIN_NAME = "custom"

    def update(self, metadata: dict) -> None:
        metadata["version"] = os.environ.get(
            "SHINYHUB_WHEEL_VERSION", "0.0.0+local"
        )


class PlatformTagHook(BuildHookInterface):
    """Includes the Go binary and sets the wheel's platform tag.

    Runs for the wheel target only; sdist builds skip this hook, so
    publishing an sdist does not require the binary to be present.
    """

    PLUGIN_NAME = "custom"

    def initialize(self, version: str, build_data: dict) -> None:
        binary = os.path.join(self.root, "src", "shinyhub", "_binary", "shinyhub")
        if not os.path.isfile(binary):
            raise RuntimeError(UNSUPPORTED_PLATFORM_MESSAGE)

        build_data["force_include"][binary] = "shinyhub/_binary/shinyhub"

        platform = os.environ.get("SHINYHUB_WHEEL_PLATFORM")
        if not platform:
            # Local sanity builds: let hatchling assign a default. CI always
            # sets this, so the production path never hits this branch.
            return
        build_data["tag"] = f"py3-none-{platform}"
        build_data["pure_python"] = False
