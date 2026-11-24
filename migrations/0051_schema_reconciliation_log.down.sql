DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM schema_reconciliation_log) THEN
    RAISE EXCEPTION 'Schema reconciliation provenance must be retained; use a forward fix';
  END IF;
END $$;
DROP TABLE schema_reconciliation_log;
