-- deploy: expand
ALTER TABLE gift_card_batches
  ADD COLUMN deleted_at TIMESTAMPTZ;

CREATE INDEX gift_card_batches_visible_brand_created_idx
  ON gift_card_batches(marca_id, created_at DESC)
  WHERE deleted_at IS NULL;
