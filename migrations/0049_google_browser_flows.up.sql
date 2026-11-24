-- deploy: expand
CREATE TABLE google_browser_flows (
  id UUID PRIMARY KEY,
  nonce_hash BYTEA NOT NULL CHECK (octet_length(nonce_hash)=32),
  verifier_hash BYTEA NOT NULL CHECK (octet_length(verifier_hash)=32),
  expires_at TIMESTAMPTZ NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ready','cancelled','failed','used')),
  proof BYTEA,
  CHECK ((status='ready' AND proof IS NOT NULL AND octet_length(proof)>28)
      OR (status<>'ready' AND proof IS NULL))
);
CREATE INDEX google_browser_flows_expiry_idx ON google_browser_flows(expires_at,id);
