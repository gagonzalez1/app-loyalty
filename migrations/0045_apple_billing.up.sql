-- deploy: expand
CREATE TABLE apple_billing_accounts (
 marca_id bigint PRIMARY KEY REFERENCES marcas(id) ON DELETE RESTRICT,
 app_account_token uuid NOT NULL UNIQUE,
 mode text NOT NULL DEFAULT 'FREE' CHECK (mode IN ('FREE','APPLE')),
 active_environment text CHECK(active_environment IN ('Production','Sandbox','Xcode')),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE apple_purchase_intents (
 id uuid PRIMARY KEY,
 marca_id bigint NOT NULL REFERENCES apple_billing_accounts(marca_id),
 actor_id bigint NOT NULL REFERENCES usuarios(id),
 idempotency_key uuid NOT NULL,
 fingerprint bytea NOT NULL,
 product_id text NOT NULL,
 offer_id text,
 offer_code text,
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(marca_id,idempotency_key)
);
CREATE TABLE apple_subscription_chains (
 environment text NOT NULL CHECK(environment IN ('Production','Sandbox','Xcode')),
 original_transaction_id text NOT NULL,
 marca_id bigint NOT NULL REFERENCES apple_billing_accounts(marca_id),
 created_at timestamptz NOT NULL DEFAULT now(),
 next_reconcile_at timestamptz NOT NULL DEFAULT now()+interval '5 minutes',
 reconcile_lease_until timestamptz,
 PRIMARY KEY(environment,original_transaction_id)
);
CREATE TABLE apple_transactions (
 environment text NOT NULL,
 transaction_id text NOT NULL,
 original_transaction_id text NOT NULL,
 product_id text NOT NULL,
 program_type text NOT NULL CHECK(program_type IN ('SELLOS','PUNTOS')),
 capacity integer NOT NULL CHECK(capacity BETWEEN 1 AND 10),
 purchase_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 signed_at timestamptz NOT NULL,
 revoked_at timestamptz,
 upgraded boolean NOT NULL DEFAULT false,
 currency char(3),
 price_milliunits bigint NOT NULL CHECK(price_milliunits>=0),
 offer_id text,
 payload jsonb NOT NULL,
 PRIMARY KEY(environment,transaction_id),
 FOREIGN KEY(environment,original_transaction_id) REFERENCES apple_subscription_chains(environment,original_transaction_id)
);
CREATE INDEX apple_transactions_chain_idx ON apple_transactions(environment,original_transaction_id,purchase_at DESC);
CREATE TABLE apple_renewal_state (
 environment text NOT NULL,
 original_transaction_id text NOT NULL,
 auto_renew boolean NOT NULL,
 pending_product_id text,
 apple_grace_ends_at timestamptz,
 signed_at timestamptz NOT NULL,
 PRIMARY KEY(environment,original_transaction_id),
 FOREIGN KEY(environment,original_transaction_id) REFERENCES apple_subscription_chains(environment,original_transaction_id)
);
CREATE TABLE apple_notification_inbox (
 environment text NOT NULL,
 notification_uuid uuid NOT NULL,
 payload jsonb NOT NULL,
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 completed_at timestamptz,
 lease_until timestamptz,
 last_error text,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(environment,notification_uuid)
);
CREATE INDEX apple_notification_pending_idx ON apple_notification_inbox(next_attempt_at) WHERE completed_at IS NULL;
-- Offer codes are operational configuration. No synthetic production prices/codes.
CREATE TABLE apple_referral_offers (
 campaign_id bigint NOT NULL REFERENCES referral_campaigns(id),
 product_id text NOT NULL,
 environment text NOT NULL CHECK(environment IN ('Production','Sandbox','Xcode')),
 offer_id text NOT NULL,
 code text NOT NULL,
 discount_bps integer NOT NULL CHECK(discount_bps BETWEEN 0 AND 9999),
 discount_charges integer NOT NULL CHECK(discount_charges BETWEEN 1 AND 36),
 enabled boolean NOT NULL DEFAULT false,
 PRIMARY KEY(campaign_id,product_id,environment,discount_charges)
);
-- Provider-separated ledger: StoreKit prices are milliunits and can use any storefront currency.
CREATE TABLE apple_referral_charges (
 environment text NOT NULL,
 transaction_id text NOT NULL,
 marca_id bigint NOT NULL REFERENCES marcas(id),
 currency char(3) NOT NULL,
 amount_milliunits bigint NOT NULL CHECK(amount_milliunits>=0),
 reward_milliunits bigint NOT NULL CHECK(reward_milliunits>=0),
 paid_index integer NOT NULL,
 source_kind text NOT NULL,
 influencer_id bigint,
 source_brand_id bigint,
 status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','SETTLED','VOID','RECOVERY_DUE')),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(environment,transaction_id),
 FOREIGN KEY(environment,transaction_id) REFERENCES apple_transactions(environment,transaction_id)
);

CREATE TABLE apple_claim_reservations (
 environment text NOT NULL,
 original_transaction_id text NOT NULL,
 marca_id bigint NOT NULL REFERENCES apple_billing_accounts(marca_id),
 app_account_token uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(environment,original_transaction_id)
);
