CREATE TABLE push_tokens (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  usuario_id BIGINT NOT NULL REFERENCES usuarios(id) ON DELETE CASCADE,
  device_id VARCHAR(128),
  token VARCHAR(255) NOT NULL UNIQUE,
  activo BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (device_id IS NULL OR btrim(device_id) <> '')
);

CREATE UNIQUE INDEX push_tokens_user_device_idx ON push_tokens(usuario_id, device_id) WHERE device_id IS NOT NULL;
CREATE INDEX push_tokens_user_active_idx ON push_tokens(usuario_id) WHERE activo;

CREATE TABLE push_notifications (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  token_id BIGINT NOT NULL REFERENCES push_tokens(id) ON DELETE CASCADE,
  event_txid BIGINT NOT NULL DEFAULT txid_current(),
  estado TEXT NOT NULL DEFAULT 'PENDING' CHECK (estado IN ('PENDING', 'SENDING', 'SENT', 'FAILED')),
  intentos INTEGER NOT NULL DEFAULT 0 CHECK (intentos >= 0),
  disponible_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  lease_until TIMESTAMPTZ,
  ultimo_error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX push_notifications_transaction_idx ON push_notifications(token_id, event_txid);
CREATE INDEX push_notifications_claim_idx ON push_notifications(disponible_at, id) WHERE estado IN ('PENDING', 'SENDING');

CREATE FUNCTION enqueue_card_push() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  customer_id BIGINT;
BEGIN
  IF TG_OP = 'DELETE' THEN
    customer_id := OLD.usuario_id;
  ELSE
    customer_id := NEW.usuario_id;
  END IF;
  INSERT INTO push_notifications(token_id)
    SELECT id FROM push_tokens WHERE usuario_id=customer_id AND activo
    ON CONFLICT(token_id,event_txid) DO NOTHING;
  IF TG_OP = 'UPDATE' AND OLD.usuario_id <> NEW.usuario_id THEN
    INSERT INTO push_notifications(token_id)
      SELECT id FROM push_tokens WHERE usuario_id=OLD.usuario_id AND activo
      ON CONFLICT(token_id,event_txid) DO NOTHING;
  END IF;
  RETURN NULL;
END;
$$;

CREATE TRIGGER tarjetas_push_updated
AFTER INSERT OR UPDATE OR DELETE ON tarjetas
FOR EACH ROW EXECUTE FUNCTION enqueue_card_push();

-- The card response also includes brand, program, benefit and artwork data.
CREATE FUNCTION enqueue_brand_card_push(brand_id BIGINT) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO push_notifications(token_id)
    SELECT DISTINCT pt.id FROM push_tokens pt
    JOIN tarjetas t ON t.usuario_id=pt.usuario_id AND t.activo
    WHERE t.marca_id=brand_id AND pt.activo
    ON CONFLICT(token_id,event_txid) DO NOTHING;
END;
$$;

CREATE FUNCTION enqueue_brand_push() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    PERFORM enqueue_brand_card_push(OLD.id);
  ELSE
    PERFORM enqueue_brand_card_push(NEW.id);
  END IF;
  RETURN NULL;
END;
$$;
CREATE TRIGGER marcas_push_updated
AFTER UPDATE ON marcas
FOR EACH ROW EXECUTE FUNCTION enqueue_brand_push();

CREATE FUNCTION enqueue_program_push() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    PERFORM enqueue_brand_card_push(OLD.marca_id);
  ELSE
    PERFORM enqueue_brand_card_push(NEW.marca_id);
  END IF;
  RETURN NULL;
END;
$$;
CREATE TRIGGER programas_push_updated
AFTER INSERT OR UPDATE OR DELETE ON programas_fidelidad
FOR EACH ROW EXECUTE FUNCTION enqueue_program_push();

CREATE FUNCTION enqueue_benefit_push() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  brand_id BIGINT;
BEGIN
  IF TG_OP = 'DELETE' THEN
    SELECT marca_id INTO brand_id FROM programas_fidelidad WHERE id=OLD.programa_id;
  ELSE
    SELECT marca_id INTO brand_id FROM programas_fidelidad WHERE id=NEW.programa_id;
  END IF;
  IF brand_id IS NOT NULL THEN
    PERFORM enqueue_brand_card_push(brand_id);
  END IF;
  RETURN NULL;
END;
$$;
CREATE TRIGGER beneficios_push_updated
AFTER INSERT OR UPDATE OR DELETE ON beneficios
FOR EACH ROW EXECUTE FUNCTION enqueue_benefit_push();

CREATE FUNCTION enqueue_artwork_push() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP <> 'INSERT' THEN
    IF OLD.tipo IN ('LOGO','BENEFICIO') AND OLD.estado='ACTIVA' THEN
      PERFORM enqueue_brand_card_push(OLD.marca_id);
    END IF;
  END IF;
  IF TG_OP <> 'DELETE' THEN
    IF NEW.tipo IN ('LOGO','BENEFICIO') AND NEW.estado='ACTIVA' THEN
      PERFORM enqueue_brand_card_push(NEW.marca_id);
    END IF;
  END IF;
  RETURN NULL;
END;
$$;
CREATE TRIGGER archivos_push_updated
AFTER INSERT OR UPDATE OR DELETE ON archivos_marca
FOR EACH ROW EXECUTE FUNCTION enqueue_artwork_push();
