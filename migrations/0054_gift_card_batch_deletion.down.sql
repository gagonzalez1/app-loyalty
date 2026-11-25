DROP INDEX IF EXISTS gift_card_batches_visible_brand_created_idx;
ALTER TABLE gift_card_batches DROP COLUMN IF EXISTS deleted_at;
