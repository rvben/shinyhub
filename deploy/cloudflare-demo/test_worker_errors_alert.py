"""Regression cases for the alert's distinction between summaries and errors."""
import pathlib
import sqlite3
import unittest


class WorkerErrorsAlertTest(unittest.TestCase):
    def test_expected_503_summaries_are_excluded_without_hiding_failures(self):
        sql = pathlib.Path(__file__).with_name("worker-errors-alert.sql").read_text()
        cases = [
            ("normal request", "info", None, 200, "cf-worker-event", 0),
            ("intentional asleep 503", "error", "GET /api/about", 503, "cf-worker-event", 0),
            ("intentional readiness 503", "error", "GET /__demo/ready", 503, "cf-worker-event", 0),
            ("upstream failure log", "error", None, None, "cf-worker-log", 1),
            ("startup failure", "error", "port timeout", None, "cf-worker-error", 1),
            ("allocation exception", "error", "no instance", 503, "cf-worker-error", 1),
            ("recorded exception", "info", "exception", None, "cf-worker-error", 1),
            ("unexpected 500 summary", "error", "GET /", 500, "cf-worker-event", 1),
            ("unexpected 502 summary", "info", None, 502, "cf-worker-event", 1),
            ("fatal log", "fatal", None, None, "cf-worker-log", 1),
            ("unknown log type", "error", "failure", 503, None, 1),
        ]
        with sqlite3.connect(":memory:") as db:
            db.execute("ATTACH DATABASE ':memory:' AS logs")
            db.execute("CREATE TABLE logs.workersLogs(accountTag, scriptName, level, error, httpStatus, logType)")
            for label, level, error, status, kind, expected in cases:
                with self.subTest(label=label):
                    db.execute("DELETE FROM logs.workersLogs")
                    db.execute("INSERT INTO logs.workersLogs VALUES (?, ?, ?, ?, ?, ?)",
                               ("d39e08a123830e48534a95a0116442dd", "shinyhub-demo", level, error, status, kind))
                    self.assertEqual(db.execute(sql).fetchone()[0], expected)
            for account, script in [("other-account", "shinyhub-demo"),
                                    ("d39e08a123830e48534a95a0116442dd", "other-worker")]:
                db.execute("DELETE FROM logs.workersLogs")
                db.execute("INSERT INTO logs.workersLogs VALUES (?, ?, 'fatal', 'failure', 500, 'cf-worker-log')",
                           (account, script))
                self.assertEqual(db.execute(sql).fetchone()[0], 0)


if __name__ == "__main__":
    unittest.main()
