# Branch proration local review

Implementation is gated and disabled by default. A brand has one recurring subscription. Staff
accounts do not increase the subscription quantity. Only a current owner may
quote, confirm, or inspect a billing operation.

Enable `BRANCH_PRORATION_ENABLED=true` and `BRANCH_PAYMENT_SIMULATOR=true` with
`APP_ENV=development`, a separate local PostgreSQL database, no Mercado Pago
access token, `MERCADO_PAGO_PROVIDER=disabled`, and a loopback `PUBLIC_APP_URL`.
The simulator server binds to 127.0.0.1; production activation is rejected.
Schema 0034 stores immutable quotes, draft branches, idempotent operations,
simulated payments, retry state, and historical recurring quantities.

HTTP routes (standard authenticated response envelope):

- POST `/v1/marcas/{brand_id}/sucursales/cotizacion`: existing CreateBranch body.
- POST `/v1/marcas/{brand_id}/sucursales/altas`: `{quote_id}` and UUID Idempotency-Key.
- GET `/v1/marcas/{brand_id}/sucursales/altas/{operation_id}`: persisted result.
- POST `/v1/marcas/{brand_id}/sucursales/altas/{operation_id}/simulacion`:
  `{action}`: approve, reject, pending, update_fail, recover, permanent_fail,
  refund_fail. These controls cannot execute in production.

Quote amounts are integer ARS cents. Argentina civil days include the creation
day and exclude the renewal date. Month end is clamped using the contracted
billing anchor (first paid/trial-end date or subscription creation date), rather
than subtracting a Go month from February 28. A quote expires after ten minutes
or local midnight. Payment initiated within a valid quote can retry a failed
plan update after the quote deadline. Trial-period additions have zero immediate
charge. The existing contracted unit price is reused, including current referral
discount; global price changes are outside this feature.

The branch is inserted active only in the same transaction that updates local
subscription quantity after a verified provider amount update. Owner status,
brand activity, quantity, date, and price snapshot are rechecked. Simulation
payment and provider failures persist across restart. Pending or retrying
operations do not create a branch. A failed update triggers refund; a failed
refund remains pending. Repeating a confirmation key returns its original
operation even after expiration or completion. Only one active billing operation
per brand may reserve a quantity change. One-off branch charges never consume a
referral discount charge.

Mercado Pago Checkout Pro, payment readback, refund and recurring-amount adapters
are included for future provider certification. They are not certified for live
billing, and production activation is hard-disabled. Duplicate approved payments for the same reference are reimbursed with stable
provider keys and readback before activation. Live provider
webhook delivery, arbitrary cycle anchors before this feature, late-settlement
notification delivery, and commercial renewal timing must be
verified separately before lifting the production gate. Local provider validation used an isolated Mercado Pago application with test
buyer/seller accounts and fictitious cards, without real funds. Approval, rejection,
plan-update recovery and duplicate webhook handling were verified. Automated
POST refunds still returns HTTP 401 with cause 7 in that isolated application;
a manual provider refund was detected correctly by reconciliation. Delayed real
provider settlement and automatic refunds remain uncertified. Testing must keep
BRANCH_PRORATION_ENABLED=false and BRANCH_PAYMENT_SIMULATOR=false; local
credentials and fixture data must not be transferred to testing.

Migration 0034 is expand-only and intentionally refuses destructive rollback to
preserve financial history. Restore the prior application with the feature off
or apply a forward fix. Default-off operation preserves the old subscription
branch guard. The separate local review database is disposable.
