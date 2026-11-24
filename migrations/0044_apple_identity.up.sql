-- deploy: expand
-- Apple subjects are explicit identities, never an email-based merge selector.
CREATE TABLE apple_identities (
  usuario_id BIGINT PRIMARY KEY REFERENCES usuarios(id),
  subject TEXT NOT NULL UNIQUE CHECK (length(subject) BETWEEN 1 AND 255),
  email CITEXT NOT NULL,
  private_relay BOOLEAN NOT NULL DEFAULT false,
  client_id TEXT NOT NULL,
  token_type TEXT NOT NULL CHECK (token_type IN ('refresh_token','access_token')),
  token_ciphertext BYTEA NOT NULL CHECK (octet_length(token_ciphertext)>16),
  token_nonce BYTEA NOT NULL CHECK (octet_length(token_nonce)=12),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE apple_auth_challenges (
  id UUID PRIMARY KEY,
  nonce_hash BYTEA NOT NULL CHECK (octet_length(nonce_hash)=32),
  purpose TEXT NOT NULL CHECK (purpose IN ('login','link','reauthenticate')),
  usuario_id BIGINT REFERENCES usuarios(id),
  session_id UUID REFERENCES sesiones_auth(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ,
  CHECK (expires_at>created_at AND expires_at<=created_at+interval '5 minutes'),
  CHECK ((purpose='login' AND usuario_id IS NULL AND session_id IS NULL)
      OR (purpose<>'login' AND usuario_id IS NOT NULL AND session_id IS NOT NULL))
);
CREATE INDEX apple_auth_challenges_expiry_idx ON apple_auth_challenges(expires_at);

-- The queue intentionally has no account/subject/email: deletion removes those
-- links atomically and leaves only encrypted provider material until revoked.
CREATE TABLE apple_identity_revocations (
  id UUID PRIMARY KEY,
  client_id TEXT NOT NULL,
  token_type TEXT NOT NULL CHECK (token_type IN ('refresh_token','access_token')),
  token_ciphertext BYTEA NOT NULL CHECK (octet_length(token_ciphertext)>16),
  token_nonce BYTEA NOT NULL CHECK (octet_length(token_nonce)=12),
  requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts>=0),
  lease_owner UUID,
  lease_until TIMESTAMPTZ,
  last_error TEXT,
  CHECK ((lease_owner IS NULL)=(lease_until IS NULL))
);
CREATE INDEX apple_identity_revocations_due_idx ON apple_identity_revocations(available_at);
