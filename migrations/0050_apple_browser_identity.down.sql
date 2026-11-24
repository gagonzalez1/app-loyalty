DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM apple_browser_credentials) THEN
    RAISE EXCEPTION 'Apple browser credentials must be retained for revocation';
  END IF;
END $$;
DROP TABLE apple_browser_credentials;
DROP TABLE apple_browser_flows;
