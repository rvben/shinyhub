-- ephemeral_data_ack records that the operator explicitly accepted ephemeral,
-- task-local app-data for this app. It is the escape hatch for the durable-data
-- guard: when 1, deploying a data-using app onto a Fargate tier whose task
-- storage is ephemeral (no S3 Files / durable_data backend) is allowed instead
-- of rejected, and pushing data to it is allowed. 0 = not acknowledged
-- (default): the guard blocks so app-data is never silently lost.
ALTER TABLE apps ADD COLUMN ephemeral_data_ack INTEGER NOT NULL DEFAULT 0;
