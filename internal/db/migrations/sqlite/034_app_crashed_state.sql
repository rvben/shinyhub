-- Records why an app is in the "crashed" status (its replicas could not be
-- brought up) so the dashboard and proxy can show the reason and a Restart
-- action instead of a silent "stopped" and an endless loading spinner.
-- last_error holds a short diagnostic (boot error + the tail of the app log,
-- e.g. a Python traceback); crashed_at is the unix epoch of the transition.
-- Both are cleared on a successful (re)start. Empty/0 = not crashed.
ALTER TABLE apps ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN crashed_at INTEGER NOT NULL DEFAULT 0;
