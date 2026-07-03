-- Worker incarnation: a monotonic counter bumped each time the control plane
-- reaps a worker (marks it down and reassigns its replicas). The worker echoes
-- its known incarnation on every heartbeat; a reported incarnation behind the
-- stored one means the worker was reaped while partitioned and must self-fence.
-- 0 = legacy (pre-fence) rows, treated as "never fence" for rolling upgrades;
-- new registrations start at 1.
ALTER TABLE workers ADD COLUMN incarnation INTEGER NOT NULL DEFAULT 0;
