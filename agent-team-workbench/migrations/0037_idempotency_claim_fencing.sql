-- 0037_idempotency_claim_fencing.sql — fence an in-flight HTTP claim.
--
-- A NULL status_code still denotes an in-progress placeholder.  The token is
-- the request owner fence; completed rows clear it so it never becomes a
-- durable credential.  Existing in-flight rows are assigned a one-off token
-- and an explicit expiry so an upgrade does not leave an unfinishable
-- placeholder behind. created_at remains the immutable creation timestamp.
ALTER TABLE idempotency_keys ADD COLUMN claim_token TEXT;
ALTER TABLE idempotency_keys ADD COLUMN claim_expires_at DATETIME;

UPDATE idempotency_keys
SET claim_token = CASE
        WHEN claim_token IS NULL OR length(trim(claim_token)) = 0
        THEN 'legacy-' || lower(hex(randomblob(16)))
        ELSE claim_token
    END,
    claim_expires_at = '1970-01-01T00:00:00Z'
WHERE status_code IS NULL
  AND (claim_token IS NULL OR length(trim(claim_token)) = 0);

UPDATE idempotency_keys
SET claim_expires_at = '1970-01-01T00:00:00Z'
WHERE status_code IS NULL AND claim_expires_at IS NULL;

CREATE UNIQUE INDEX idempotency_keys_active_claim_token
    ON idempotency_keys(claim_token)
    WHERE status_code IS NULL AND claim_token IS NOT NULL;

CREATE TRIGGER idempotency_active_claim_token_insert_guard
BEFORE INSERT ON idempotency_keys
WHEN NEW.status_code IS NULL
 AND (NEW.claim_token IS NULL OR length(trim(NEW.claim_token)) = 0
      OR NEW.claim_expires_at IS NULL)
 OR (NEW.status_code IS NOT NULL
     AND (NEW.claim_token IS NOT NULL OR NEW.claim_expires_at IS NOT NULL))
BEGIN
    SELECT RAISE(ABORT, 'in-progress idempotency claim requires token and expiry');
END;

CREATE TRIGGER idempotency_active_claim_token_update_guard
BEFORE UPDATE OF status_code, claim_token, claim_expires_at ON idempotency_keys
WHEN (NEW.status_code IS NULL
      AND (NEW.claim_token IS NULL OR length(trim(NEW.claim_token)) = 0
           OR NEW.claim_expires_at IS NULL))
  OR (NEW.status_code IS NOT NULL AND NEW.claim_expires_at IS NOT NULL)
  OR (NEW.status_code IS NOT NULL AND NEW.claim_token IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'idempotency claim token/expiry does not match status');
END;

CREATE TRIGGER idempotency_created_at_immutable
BEFORE UPDATE OF created_at ON idempotency_keys
WHEN OLD.created_at IS NOT NEW.created_at
BEGIN
    SELECT RAISE(ABORT, 'idempotency created_at is immutable');
END;
