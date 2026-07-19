LOCK TABLE time_entries IN SHARE ROW EXCLUSIVE MODE;

WITH active_entries AS (
    SELECT
        id,
        LEAD(clock_in) OVER (
            PARTITION BY user_id
            ORDER BY clock_in, id
        ) AS next_clock_in
    FROM time_entries
    WHERE clock_out IS NULL
)
UPDATE time_entries
SET clock_out = active_entries.next_clock_in
FROM active_entries
WHERE time_entries.id = active_entries.id
  AND active_entries.next_clock_in IS NOT NULL;

CREATE UNIQUE INDEX time_entries_one_active_per_user
    ON time_entries(user_id)
    WHERE clock_out IS NULL;
