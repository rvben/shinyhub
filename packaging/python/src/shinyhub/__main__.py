"""Entry point for the ``shinyhub`` console script.

We locate the embedded Go binary inside the installed package data and
``os.execv`` to replace this Python process with it. Process replacement
(rather than ``subprocess.run``) preserves signal handling, exit codes,
and stdio, so ``shinyhub`` behaves indistinguishably from the native
binary for every caller.

argv[0] must be the binary's own absolute path: the server re-execs
argv[0] on SIGHUP for zero-downtime upgrades, and a bare name only
resolves if a ``shinyhub`` happens to be on the service's PATH.
"""
import os
import sys
from importlib.resources import files


def main() -> None:
    binary = files("shinyhub") / "_binary" / "shinyhub"
    if not binary.is_file():
        sys.stderr.write(
            "shinyhub: embedded binary not found at "
            f"{binary}. This wheel is broken; please report it at "
            "https://github.com/rvben/shinyhub/issues.\n"
        )
        sys.exit(1)
    os.execv(str(binary), [str(binary), *sys.argv[1:]])


if __name__ == "__main__":
    main()
