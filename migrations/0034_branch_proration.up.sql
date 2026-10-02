-- Quotes are immutable snapshots; branch drafts remain here until billing is confirmed.
CREATE TABLE branch_quotes (
 id UUID PRIMARY KEY, marca_id BIGINT NOT NULL REFERENCES marcas(id), actor_id BIGINT NOT NULL REFERENCES usuarios(id),
 quote JSONB NOT NULL, draft JSONB NOT NULL, subscription_snapshot JSONB NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE branch_operations (
 id UUID PRIMARY KEY, marca_id BIGINT NOT NULL REFERENCES marcas(id), actor_id BIGINT NOT NULL REFERENCES usuarios(id),
 quote_id UUID NOT NULL UNIQUE REFERENCES branch_quotes(id), idempotency_key UUID NOT NULL,
 status TEXT NOT NULL CHECK(status IN('PAYMENT_PENDING','PLAN_UPDATING','COMPLETED','PAYMENT_REJECTED','REFUND_PENDING','REFUNDED','FAILED')),
 branch_id BIGINT UNIQUE REFERENCES sucursales(id), checkout_url TEXT, checkout_id TEXT,
 payment_id TEXT, payment_status TEXT NOT NULL DEFAULT 'pending', payment_amount_minor BIGINT NOT NULL DEFAULT 0,
 payment_refunded_minor BIGINT NOT NULL DEFAULT 0, provider_plan_amount_minor BIGINT, payment_approved_at TIMESTAMPTZ, plan_update_started BOOLEAN NOT NULL DEFAULT false,
 simulation BOOLEAN NOT NULL DEFAULT false, simulation_fault TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0, error_code TEXT NOT NULL DEFAULT '',
 next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(), created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(marca_id,idempotency_key)
);
CREATE UNIQUE INDEX branch_operations_single_active_brand ON branch_operations(marca_id) WHERE status IN('PAYMENT_PENDING','PLAN_UPDATING','REFUND_PENDING');
CREATE INDEX branch_operations_reconcile ON branch_operations(next_attempt_at) WHERE status IN('PAYMENT_PENDING','PLAN_UPDATING','REFUND_PENDING');

CREATE TABLE subscription_quantity_history (
 id BIGSERIAL PRIMARY KEY, marca_id BIGINT NOT NULL REFERENCES marcas(id), operation_id UUID NOT NULL UNIQUE REFERENCES branch_operations(id),
 unit_price_minor BIGINT NOT NULL, full_unit_price_minor BIGINT NOT NULL, branches BIGINT NOT NULL, valid_from TIMESTAMPTZ NOT NULL, valid_until TIMESTAMPTZ NOT NULL
);
