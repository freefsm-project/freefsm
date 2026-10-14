LOCK TABLE time_entries IN SHARE ROW EXCLUSIVE MODE;

-- A later start does not prove when an earlier clock ended. Stop without
-- changing any recorded interval; require explicit reviewed historical repair.
DO $$ BEGIN
 IF EXISTS(SELECT user_id FROM time_entries WHERE clock_out IS NULL GROUP BY user_id HAVING count(*)>1) THEN
  RAISE EXCEPTION 'Time cutover blocked: multiple recorded active entries require explicit Administrator-reviewed historical repair; no end time has been inferred';
 END IF;
END $$;

CREATE UNIQUE INDEX time_entries_one_active_per_user
    ON time_entries(user_id)
    WHERE clock_out IS NULL;
