-- Operational gate: drain pending revocations before rolling this schema back.
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM apple_identity_revocations) THEN
    RAISE EXCEPTION 'pending Apple revocations must be drained before rollback';
  END IF;
  IF EXISTS (SELECT 1 FROM apple_identities) THEN
    RAISE EXCEPTION 'linked Apple identities require a forward fix, not destructive rollback';
  END IF;
END $$;
DROP TABLE apple_identity_revocations;
DROP TABLE apple_auth_challenges;
DROP TABLE apple_identities;
