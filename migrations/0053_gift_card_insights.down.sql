ALTER TABLE gift_card_codes
  DROP CONSTRAINT IF EXISTS gift_card_codes_encrypted_token_pair,
  DROP COLUMN IF EXISTS token_nonce,
  DROP COLUMN IF EXISTS token_ciphertext;

