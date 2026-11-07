-- deploy: expand
ALTER TABLE usuarios ADD COLUMN last_app_opened_at TIMESTAMPTZ;
