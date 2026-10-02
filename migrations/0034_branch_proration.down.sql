-- Financial operation history must not be discarded, including pending refunds.
DO $$ BEGIN RAISE EXCEPTION '0034 contains billing operation history; use a forward fix'; END $$;
