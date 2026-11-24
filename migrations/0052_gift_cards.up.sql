CREATE TABLE gift_card_batches (
  id UUID PRIMARY KEY,
  marca_id BIGINT NOT NULL REFERENCES marcas(id),
  sucursal_id BIGINT NOT NULL REFERENCES sucursales(id),
  programa_id BIGINT NOT NULL REFERENCES programas_fidelidad(id),
  creado_por BIGINT NOT NULL REFERENCES usuarios(id),
  titulo TEXT NOT NULL CHECK (char_length(btrim(titulo)) BETWEEN 1 AND 80),
  programa_tipo TEXT NOT NULL CHECK (programa_tipo IN ('PUNTOS','SELLOS')),
  cantidad BIGINT NOT NULL CHECK (cantidad BETWEEN 1 AND 100000),
  total_codigos INTEGER NOT NULL CHECK (total_codigos BETWEEN 1 AND 100),
  estado TEXT NOT NULL DEFAULT 'ACTIVA' CHECK (estado IN ('ACTIVA','REVOCADA')),
  expires_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX gift_card_batches_brand_created_idx ON gift_card_batches(marca_id, created_at DESC);

CREATE TABLE gift_card_codes (
  id UUID PRIMARY KEY,
  batch_id UUID NOT NULL REFERENCES gift_card_batches(id) ON DELETE CASCADE,
  token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash)=32),
  token_hint TEXT NOT NULL,
  redeemed_by BIGINT REFERENCES usuarios(id),
  movement_id BIGINT REFERENCES historial_movimientos(id),
  redeemed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((redeemed_at IS NULL AND redeemed_by IS NULL AND movement_id IS NULL) OR
         (redeemed_at IS NOT NULL AND redeemed_by IS NOT NULL AND movement_id IS NOT NULL))
);

CREATE INDEX gift_card_codes_batch_idx ON gift_card_codes(batch_id);

