ALTER TABLE users ADD COLUMN IF NOT EXISTS onboarding_completed_at TIMESTAMPTZ;

UPDATE invitation_tokens AS i
SET company_id = u.company_id
FROM users AS u
WHERE i.user_id = u.id
  AND i.company_id IS NULL
  AND u.company_id IS NOT NULL;

UPDATE users AS u
SET onboarding_completed_at = COALESCE(
    (
        SELECT MIN(evidence.occurred_at)
        FROM (
            SELECT a.created_at AS occurred_at
            FROM activity_logs AS a
            WHERE a.action = 'logged_in'
              AND a.object_type = 'user'
              AND a.object_id = u.id
            UNION ALL
            SELECT s.created_at
            FROM sessions AS s
            WHERE s.user_id = u.id
            UNION ALL
            SELECT i.consumed_at
            FROM invitation_tokens AS i
            WHERE i.user_id = u.id
              AND i.consumed_at IS NOT NULL
        ) AS evidence
    ),
    u.created_at
)
WHERE u.onboarding_completed_at IS NULL
  AND NOT (
      u.is_active = false
      AND u.welcome_email_sent_at IS NOT NULL
      AND EXISTS (
          SELECT 1
          FROM invitation_tokens AS i
          WHERE i.user_id = u.id
            AND i.consumed_at IS NULL
      )
      AND NOT EXISTS (
          SELECT 1
          FROM activity_logs AS a
          WHERE a.action = 'logged_in'
            AND a.object_type = 'user'
            AND a.object_id = u.id
      )
      AND NOT EXISTS (SELECT 1 FROM sessions AS s WHERE s.user_id = u.id)
  );
