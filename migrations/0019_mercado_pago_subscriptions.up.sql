CREATE TABLE suscripciones_marca (
  marca_id BIGINT PRIMARY KEY REFERENCES marcas(id) ON DELETE RESTRICT,
  proveedor TEXT NOT NULL CHECK (proveedor = 'MERCADO_PAGO'),
  proveedor_suscripcion_id TEXT UNIQUE NOT NULL,
  referencia_externa TEXT UNIQUE NOT NULL,
  estado TEXT NOT NULL CHECK (estado IN ('PENDING','AUTHORIZED','PAUSED','CANCELLED')),
  moneda CHAR(3) NOT NULL CHECK (moneda = 'ARS'),
  precio_sucursal_minor BIGINT NOT NULL CHECK (precio_sucursal_minor > 0),
  cantidad_sucursales BIGINT NOT NULL CHECK (cantidad_sucursales > 0),
  importe_mensual_minor BIGINT NOT NULL CHECK (importe_mensual_minor = precio_sucursal_minor * cantidad_sucursales),
  checkout_url TEXT,
  proximo_cobro_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE eventos_mercado_pago (
  notification_id TEXT PRIMARY KEY,
  resource_id TEXT NOT NULL,
  topic TEXT NOT NULL,
  processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX suscripciones_marca_estado_idx ON suscripciones_marca(estado);
