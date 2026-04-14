ALTER TABLE apps ADD COLUMN hibernate_timeout_minutes INTEGER;
-- NULL  = inherit global lifecycle.hibernate_timeout config
-- 0     = hibernation disabled for this app
-- N > 0 = custom idle timeout in minutes
