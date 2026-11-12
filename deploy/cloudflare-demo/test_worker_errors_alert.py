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
            db.execute("CREATE TABLE logs.workersLogs(accountTag, scriptName, level, error, httpStatus, logType, invocationType, traceId, requestId, id)")
            for label, level, error, status, kind, expected in cases:
                with self.subTest(label=label):
                    db.execute("DELETE FROM logs.workersLogs")
                    db.execute("INSERT INTO logs.workersLogs(accountTag, scriptName, level, error, httpStatus, logType, id) VALUES (?, ?, ?, ?, ?, ?, 'record')",
                               ("d39e08a123830e48534a95a0116442dd", "shinyhub-demo", level, error, status, kind))
                    self.assertEqual(db.execute(sql).fetchone()[0], expected)
            for account, script in [("other-account", "shinyhub-demo"),
                                    ("d39e08a123830e48534a95a0116442dd", "other-worker")]:
                db.execute("DELETE FROM logs.workersLogs")
                db.execute("INSERT INTO logs.workersLogs(accountTag, scriptName, level, error, httpStatus, logType, id) VALUES (?, ?, 'fatal', 'failure', 500, 'cf-worker-log', 'record')",
                           (account, script))
                self.assertEqual(db.execute(sql).fetchone()[0], 0)

    def test_lifecycle_split_preserves_all_records_and_unknown_invocations(self):
        root = pathlib.Path(__file__).parent
        request_sql = (root / "worker-errors-alert.sql").read_text()
        lifecycle_sql = (root / "worker-lifecycle-alert.sql").read_text()
        with sqlite3.connect(":memory:") as db:
            db.execute("ATTACH DATABASE ':memory:' AS logs")
            db.execute("CREATE TABLE logs.workersLogs(accountTag, scriptName, level, error, httpStatus, logType, invocationType, traceId, requestId, id)")
            rows = [
                # One failed trace across Worker and DO invocations, with duplicate logs.
                ("fetch", "trace-1", "req-1", "a"), ("fetch", "trace-1", "req-1", "b"),
                ("fetch", "trace-1", "req-2", "c"),
                # Lifecycle logs are a separate alert; absent trace falls back to request.
                ("alarm", "", "alarm-1", "d"), ("alarm", None, "alarm-1", "e"),
                # Missing identifiers/types must remain visible.
                (None, "", "", "f"), ("jsrpc", None, None, "g"),
            ]
            for kind, trace, request, record in rows:
                db.execute("INSERT INTO logs.workersLogs VALUES (?, ?, 'error', 'failure', NULL, 'cf-worker-log', ?, ?, ?, ?)",
                           ("d39e08a123830e48534a95a0116442dd", "shinyhub-demo", kind, trace, request, record))
            self.assertEqual(db.execute(request_sql).fetchone()[0], 5)
            self.assertEqual(db.execute(lifecycle_sql).fetchone()[0], 2)

    def test_lifecycle_alert_ignores_a_deployment_reset_but_no_other_alarm_failure(self):
        sql = pathlib.Path(__file__).with_name("worker-lifecycle-alert.sql").read_text()
        reset = "Durable Object reset because its code was updated."
        scheduled = "Thu Nov 12 2026 08:21:05 GMT+0000 (Coordinated Universal Time)"
        cases = [
            # A deployment produces exactly this pair, minutes after it lands.
            ("deployment reset exception", "alarm", "error", reset, None, "cf-worker-error", 0),
            ("alarm summary of that reset", "alarm", "error", scheduled, None, "cf-worker-event", 0),
            ("successful alarm summary", "alarm", "info", None, None, "cf-worker-event", 0),
            ("other alarm exception", "alarm", "error", "Network connection lost.", None, "cf-worker-error", 1),
            ("alarm error log without an error field", "alarm", "error", None, None, "cf-worker-log", 1),
            ("alarm fatal log", "alarm", "fatal", None, None, "cf-worker-log", 1),
            ("recorded alarm exception", "alarm", "info", "exception", None, "cf-worker-error", 1),
            ("unknown log type", "alarm", "error", "failure", None, None, 1),
            ("unknown log type with a 503", "alarm", "error", "failure", 503, None, 1),
            ("exception with a 503", "alarm", "error", "no instance", 503, "cf-worker-error", 1),
            ("message that only contains the reset text", "alarm", "error", reset + " Then it failed.", None, "cf-worker-error", 1),
            ("reset text in another case", "alarm", "error", reset.upper(), None, "cf-worker-error", 1),
            ("request error", "fetch", "error", "failure", None, "cf-worker-log", 0),
            ("unknown invocation type", None, "error", "failure", None, "cf-worker-log", 0),
        ]
        with sqlite3.connect(":memory:") as db:
            db.execute("ATTACH DATABASE ':memory:' AS logs")
            db.execute("CREATE TABLE logs.workersLogs(accountTag, scriptName, level, error, httpStatus, logType, invocationType, traceId, requestId, id)")
            for label, invocation, level, error, status, kind, expected in cases:
                with self.subTest(label=label):
                    db.execute("DELETE FROM logs.workersLogs")
                    db.execute("INSERT INTO logs.workersLogs(accountTag, scriptName, level, error, httpStatus, logType, invocationType, id) VALUES (?, ?, ?, ?, ?, ?, ?, 'record')",
                               ("d39e08a123830e48534a95a0116442dd", "shinyhub-demo", level, error, status, kind, invocation))
                    self.assertEqual(db.execute(sql).fetchone()[0], expected)

    def test_request_alert_still_counts_a_deployment_reset(self):
        sql = pathlib.Path(__file__).with_name("worker-errors-alert.sql").read_text()
        with sqlite3.connect(":memory:") as db:
            db.execute("ATTACH DATABASE ':memory:' AS logs")
            db.execute("CREATE TABLE logs.workersLogs(accountTag, scriptName, level, error, httpStatus, logType, invocationType, traceId, requestId, id)")
            db.execute("INSERT INTO logs.workersLogs(accountTag, scriptName, level, error, logType, invocationType, id) VALUES (?, ?, 'error', 'Durable Object reset because its code was updated.', 'cf-worker-error', 'fetch', 'record')",
                       ("d39e08a123830e48534a95a0116442dd", "shinyhub-demo"))
            self.assertEqual(db.execute(sql).fetchone()[0], 1)


if __name__ == "__main__":
    unittest.main()
