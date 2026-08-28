#!/usr/bin/env python3
"""Populate the public demo viewer's identity claims idempotently."""

import sqlite3
import sys


USERNAME = "demo-viewer"
DISPLAY_NAME = "Demo Viewer"
GROUPS = ("analytics-readers", "demo-users")


def bootstrap(database_path: str) -> None:
    with sqlite3.connect(database_path, timeout=10) as connection:
        connection.execute("PRAGMA foreign_keys = ON")
        row = connection.execute(
            "SELECT id FROM users WHERE username = ?",
            (USERNAME,),
        ).fetchone()
        if row is None:
            raise RuntimeError(f"demo viewer {USERNAME!r} does not exist")

        user_id = row[0]
        connection.execute(
            "UPDATE users SET display_name = ?, managed_by = ? WHERE id = ?",
            (DISPLAY_NAME, "public-demo", user_id),
        )
        connection.execute("DELETE FROM user_groups WHERE user_id = ?", (user_id,))
        connection.executemany(
            "INSERT INTO user_groups (user_id, group_name) VALUES (?, ?)",
            ((user_id, group) for group in GROUPS),
        )


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("usage: bootstrap-viewer.py DATABASE_PATH")
    bootstrap(sys.argv[1])
