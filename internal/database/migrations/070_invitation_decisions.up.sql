-- Migration 048 synthesized completion from creation time (or token invalidation)
-- for some failed/pending welcomes. Keep those recorded values for review rather
-- than treating them as proof of activation. Active users remain untouched.
CREATE TABLE onboarding_migration_review (
    user_id BIGINT PRIMARY KEY REFERENCES users(id),
    recorded_completed_at TIMESTAMPTZ NOT NULL,
    reason TEXT NOT NULL,
    reviewed_at TIMESTAMPTZ
);
INSERT INTO onboarding_migration_review(user_id,recorded_completed_at,reason)
SELECT u.id,u.onboarding_completed_at,'Legacy completion lacks recorded activation evidence; explicit Administrator resend required'
FROM users u
WHERE NOT u.is_active AND u.welcome_email_sent_at IS NOT NULL
  AND (u.onboarding_completed_at=u.created_at OR EXISTS (
      SELECT 1 FROM invitation_tokens i WHERE i.user_id=u.id AND i.consumed_at=u.onboarding_completed_at))
  AND NOT EXISTS (SELECT 1 FROM sessions s WHERE s.user_id=u.id)
  AND NOT EXISTS (SELECT 1 FROM activity_logs a WHERE a.object_type='user' AND a.object_id=u.id AND a.action IN ('logged_in','invite_accepted'));
UPDATE users SET onboarding_completed_at=NULL
WHERE id IN (SELECT user_id FROM onboarding_migration_review);
-- Do not make a legacy link usable by removing uncertain completion state.
UPDATE invitation_tokens SET consumed_at=clock_timestamp()
WHERE consumed_at IS NULL AND user_id IN (SELECT user_id FROM onboarding_migration_review);

-- An outstanding invitation authenticates the onboarding decision that issued
-- it, not a later identity, Role or credential decision. Retain token evidence
-- and revoke it atomically with every explicit pending-account security edit.
CREATE FUNCTION invalidate_pending_invitations() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.onboarding_completed_at IS NULL AND NOT OLD.is_active THEN
        UPDATE invitation_tokens SET consumed_at=clock_timestamp()
        WHERE user_id=OLD.id AND consumed_at IS NULL;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER pending_invitation_decision
BEFORE UPDATE OF is_active,authorization_role_id,name,email,password_hash,force_password_change ON users
FOR EACH ROW EXECUTE FUNCTION invalidate_pending_invitations();
