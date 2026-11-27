-- deploy: expand
ALTER TABLE gift_card_batches
  ADD COLUMN social_links JSONB NOT NULL DEFAULT '[]'::jsonb,
  ADD CONSTRAINT gift_card_batches_social_links_check CHECK (
    CASE
      WHEN jsonb_typeof(social_links) = 'array' THEN jsonb_array_length(social_links) <= 4
      ELSE FALSE
    END
  );
