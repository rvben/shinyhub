"""Entry point for the ``shinyhub`` console script.

We locate the embedded Go binary inside the installed package data and
``os.execv`` to replace this Python process with it. Process replacement
(rather than ``subprocess.run``) preserves signal handling, exit codes,
stdio, and $0, so ``shinyhub`` behaves indistinguishably from the native
binary for every caller.
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
    os.execv(str(binary), ["shinyhub", *sys.argv[1:]])


if __name__ == "__main__":
    main()
