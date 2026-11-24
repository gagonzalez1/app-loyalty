-- deploy: expand
CREATE TABLE apple_browser_flows (
  id UUID PRIMARY KEY,
  platform TEXT NOT NULL CHECK (platform IN ('web','native')),
  usuario_id BIGINT REFERENCES usuarios(id),
  session_family UUID,
  auth_version INTEGER,
  nonce_hash BYTEA NOT NULL CHECK (octet_length(nonce_hash)=32),
  verifier_hash BYTEA NOT NULL CHECK (octet_length(verifier_hash)=32),
  expires_at TIMESTAMPTZ NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ready','cancelled','failed','used')),
  proof BYTEA,
  proof_nonce BYTEA,
  CHECK ((usuario_id IS NULL)=(session_family IS NULL) AND (usuario_id IS NULL)=(auth_version IS NULL)),
  CHECK ((status='ready' AND proof IS NOT NULL AND octet_length(proof)>16 AND proof_nonce IS NOT NULL AND octet_length(proof_nonce)=12)
      OR (status<>'ready' AND proof IS NULL AND proof_nonce IS NULL))
);
CREATE INDEX apple_browser_flows_expiry_idx ON apple_browser_flows(expires_at);
CREATE INDEX apple_browser_flows_user_idx ON apple_browser_flows(usuario_id) WHERE usuario_id IS NOT NULL;

CREATE TABLE apple_browser_credentials (
  usuario_id BIGINT NOT NULL REFERENCES apple_identities(usuario_id),
  client_id TEXT NOT NULL,
  token_type TEXT NOT NULL CHECK (token_type IN ('refresh_token','access_token')),
  token_ciphertext BYTEA NOT NULL CHECK (octet_length(token_ciphertext)>16),
  token_nonce BYTEA NOT NULL CHECK (octet_length(token_nonce)=12),
  PRIMARY KEY (usuario_id,client_id)
);
