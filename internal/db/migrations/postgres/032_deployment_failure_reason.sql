-- Records why a deploy attempt failed so a developer can diagnose it after the
-- fact (e.g. "R runtime not found"). Empty for pending/succeeded rows and for
-- failures recorded before this column existed.
ALTER TABLE deployments ADD COLUMN failure_reason TEXT NOT NULL DEFAULT '';
