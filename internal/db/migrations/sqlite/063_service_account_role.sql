-- Service-account authority lives on each credential. Keep the shared
-- principal's compatibility role inert so it never satisfies human-admin
-- safety checks after an upgrade from the legacy deploy identity.
UPDATE users
SET role = 'developer'
WHERE principal_type = 'service_account';
