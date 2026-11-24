-- deploy: expand
ALTER TABLE gift_card_codes
  ADD COLUMN token_ciphertext BYTEA,
  ADD COLUMN token_nonce BYTEA,
  ADD CONSTRAINT gift_card_codes_encrypted_token_pair CHECK (
    (token_ciphertext IS NULL AND token_nonce IS NULL) OR
    (token_ciphertext IS NOT NULL AND token_nonce IS NOT NULL)
  );

