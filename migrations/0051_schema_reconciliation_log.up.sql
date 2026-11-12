-- deploy: expand
CREATE TABLE schema_reconciliation_log (
  id TEXT PRIMARY KEY,
  source_lineage TEXT NOT NULL,
  target_version CHAR(4) NOT NULL CHECK (target_version ~ '^[0-9]{4}$'),
  source_history JSONB NOT NULL CHECK (jsonb_typeof(source_history) = 'array'),
  operations JSONB NOT NULL CHECK (jsonb_typeof(operations) = 'array'),
  completed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
