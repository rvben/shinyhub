-- Never infer historical owner intent. Keep the default for predecessor writers.
ALTER TABLE apps DROP CONSTRAINT apps_availability_check;
ALTER TABLE apps ADD CONSTRAINT apps_availability_check CHECK (availability IN ('off', 'public', 'inherit'));
