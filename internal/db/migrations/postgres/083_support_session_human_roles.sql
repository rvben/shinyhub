ALTER TABLE support_sessions DROP CONSTRAINT IF EXISTS support_sessions_subject_role_check;
ALTER TABLE support_sessions ADD CONSTRAINT support_sessions_subject_role_check CHECK (subject_role IN ('viewer', 'developer', 'operator', 'admin'));
