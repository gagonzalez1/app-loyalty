-- Financial history and purchase ownership survive deployments and must never
-- be discarded by a rollback. Use an audited forward migration once populated.
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM apple_billing_accounts)
     OR EXISTS (SELECT 1 FROM apple_purchase_intents)
     OR EXISTS (SELECT 1 FROM apple_subscription_chains)
     OR EXISTS (SELECT 1 FROM apple_transactions)
     OR EXISTS (SELECT 1 FROM apple_renewal_state)
     OR EXISTS (SELECT 1 FROM apple_notification_inbox)
     OR EXISTS (SELECT 1 FROM apple_claim_reservations)
     OR EXISTS (SELECT 1 FROM apple_referral_charges)
     OR EXISTS (SELECT 1 FROM apple_referral_offers) THEN
    RAISE EXCEPTION USING ERRCODE = '55000',
      MESSAGE = 'Apple billing data, pending purchases or claim history require a forward fix, not destructive rollback';
  END IF;
END $$;
DROP TABLE IF EXISTS apple_claim_reservations;
DROP TABLE IF EXISTS apple_referral_charges;
DROP TABLE IF EXISTS apple_referral_offers;
DROP TABLE IF EXISTS apple_notification_inbox;
DROP TABLE IF EXISTS apple_renewal_state;
DROP TABLE IF EXISTS apple_transactions;
DROP TABLE IF EXISTS apple_subscription_chains;
DROP TABLE IF EXISTS apple_purchase_intents;
DROP TABLE IF EXISTS apple_billing_accounts;
