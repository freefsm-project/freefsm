-- Operators may create this exact input table before retrying a stopped
-- upgrade. Only explicit current product-Administrator review is accepted.
SELECT pg_advisory_xact_lock(190019);
CREATE TABLE IF NOT EXISTS job_dispatch_legacy_approvals (
 job_id bigint PRIMARY KEY REFERENCES jobs(id),
 source jsonb NOT NULL,
 plan jsonb NOT NULL,
 reviewer_id bigint NOT NULL REFERENCES users(id),
 reviewed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 reason text NOT NULL CHECK (btrim(reason)<>''),
 CHECK (jsonb_typeof(plan->'assignee_ids')='array')
);
CREATE FUNCTION job_dispatch_source(target bigint) RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT jsonb_build_object('job',to_jsonb(j),'assignments',coalesce((SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM job_assignments a WHERE a.job_id=j.id),'[]')) FROM jobs j WHERE j.id=target;
$$;
CREATE TABLE job_dispatch_legacy_inventory (
 job_id bigint PRIMARY KEY REFERENCES jobs(id),
 source jsonb NOT NULL,
 timezone text,
 reasons text[] NOT NULL
);
INSERT INTO job_dispatch_legacy_inventory
 SELECT j.id,job_dispatch_source(j.id),cs.timezone,array_remove(ARRAY[
 CASE WHEN (j.start_time IS NOT NULL OR j.end_time IS NOT NULL OR j.due_date IS NOT NULL) AND z.name IS NULL THEN 'unknown company timezone/offset' END,
 CASE WHEN (j.start_time IS NULL)<>(j.end_time IS NULL) OR j.start_time>=j.end_time THEN 'incomplete or unordered interval' END,
 CASE WHEN (j.arrival_window_start IS NULL)<>(j.arrival_window_end IS NULL) OR j.arrival_window_start IS NOT NULL AND (j.start_time IS NULL OR j.arrival_window_start>j.start_time OR j.arrival_window_end<j.start_time) THEN 'invalid arrival window' END,
 CASE WHEN j.visits<>'[]'::jsonb THEN 'legacy visits require one-interval review' END,
 CASE WHEN EXISTS(SELECT 1 FROM job_assignments a WHERE a.job_id=j.id AND a.role<>'') THEN 'assignment labels/primary semantics require equal-assignee review' END,
 CASE WHEN j.assignments<>'[]'::jsonb AND (jsonb_typeof(j.assignments)<>'array' OR EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(j.assignments)='array' THEN j.assignments ELSE '[]' END) a WHERE NOT EXISTS(SELECT 1 FROM job_assignments ja WHERE ja.job_id=j.id AND ja.user_id::text=a->>'user_id') OR coalesce(a->>'role','')<>'' OR a-ARRAY['user_id','name','role','preserved_user_id']<>'{}'::jsonb)) THEN 'legacy assignment mapping requires review' END
 ],NULL)
 FROM jobs j LEFT JOIN company_settings cs ON cs.company_id=j.company_id LEFT JOIN pg_timezone_names z ON z.name=cs.timezone;
DO $$ DECLARE missing text; BEGIN
	IF EXISTS(SELECT 1 FROM job_dispatch_legacy_approvals a JOIN jobs j ON j.id=a.job_id WHERE a.source<>job_dispatch_source(j.id)
	 OR NOT(a.plan ?& ARRAY['start','end','arrival_start','arrival_end','due_date','assignee_ids'])
	 OR NOT EXISTS(SELECT 1 FROM users u JOIN authorization_roles r ON r.id=u.authorization_role_id WHERE u.id=a.reviewer_id AND u.company_id=j.company_id AND u.is_active AND NOT u.force_password_change AND r.protected)) THEN
	 RAISE EXCEPTION 'Job dispatch requires Administrator review: approval is stale, incomplete or lacks current protected Administrator identity'; END IF;
 SELECT string_agg(i.job_id::text,', ' ORDER BY i.job_id) INTO missing
 FROM job_dispatch_legacy_inventory i WHERE cardinality(i.reasons)>0 AND NOT EXISTS(
 SELECT 1 FROM job_dispatch_legacy_approvals a JOIN users u ON u.id=a.reviewer_id JOIN authorization_roles r ON r.id=u.authorization_role_id
 WHERE a.job_id=i.job_id AND a.source=i.source AND u.is_active AND r.protected AND u.company_id=(i.source->'job'->>'company_id')::bigint);
 IF missing IS NOT NULL THEN RAISE EXCEPTION 'Job dispatch requires Administrator review for Jobs %: unknown timezone, invalid interval/window, legacy visits or assignment semantics. Preserve and explicitly map source records using docs/job-dispatch.md; nothing was changed.',missing; END IF;
END $$;
-- Approved mappings are prospective corrections with their original source
-- retained. They are not historical overlap acknowledgments or new grants.
UPDATE jobs j SET start_time=(a.plan->>'start')::timestamptz,end_time=(a.plan->>'end')::timestamptz,
 arrival_window_start=(a.plan->>'arrival_start')::timestamptz,arrival_window_end=(a.plan->>'arrival_end')::timestamptz,
 due_date=(a.plan->>'due_date')::date::timestamp AT TIME ZONE 'UTC'
 FROM job_dispatch_legacy_approvals a WHERE a.job_id=j.id AND a.source=job_dispatch_source(j.id);
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM job_dispatch_legacy_approvals a WHERE (a.plan->>'start' IS NULL)<>(a.plan->>'end' IS NULL)
 OR (a.plan->>'start')::timestamptz>=(a.plan->>'end')::timestamptz
 OR (a.plan->>'arrival_start' IS NULL)<>(a.plan->>'arrival_end' IS NULL)
 OR a.plan->>'arrival_start' IS NOT NULL AND (a.plan->>'start' IS NULL OR (a.plan->>'arrival_start')::timestamptz>(a.plan->>'start')::timestamptz OR (a.plan->>'arrival_end')::timestamptz<(a.plan->>'start')::timestamptz)
 OR EXISTS(SELECT 1 FROM jsonb_array_elements_text(a.plan->'assignee_ids') uid WHERE NOT EXISTS(SELECT 1 FROM users u JOIN jobs j ON j.company_id=u.company_id WHERE j.id=a.job_id AND u.id=uid::bigint))
 OR (a.plan->>'due_date' IS NOT NULL AND a.plan->>'due_date' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$')
 OR (a.plan->>'start' IS NOT NULL AND a.plan->>'start' !~ '(Z|[+-][0-9]{2}:[0-9]{2})$')
 OR (a.plan->>'end' IS NOT NULL AND a.plan->>'end' !~ '(Z|[+-][0-9]{2}:[0-9]{2})$')
 OR (a.plan->>'arrival_start' IS NOT NULL AND a.plan->>'arrival_start' !~ '(Z|[+-][0-9]{2}:[0-9]{2})$')
 OR (a.plan->>'arrival_end' IS NOT NULL AND a.plan->>'arrival_end' !~ '(Z|[+-][0-9]{2}:[0-9]{2})$')) THEN
 RAISE EXCEPTION 'Job dispatch requires Administrator review: mapping must have explicit offset instants, ordered interval, containing arrival window and same-company assignees'; END IF;
END $$;
-- Keep existing assignment identifiers and legacy labels in historical data.
-- New normalized relationships come only from the explicitly reviewed mapping.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM job_dispatch_legacy_approvals a CROSS JOIN LATERAL jsonb_array_elements_text(a.plan->'assignee_ids') selected
 JOIN users u ON u.id=selected::bigint WHERE NOT u.is_active
 AND NOT EXISTS(SELECT 1 FROM job_assignments existing WHERE existing.job_id=a.job_id AND existing.user_id=u.id)
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(a.source->'job'->'assignments')='array' THEN a.source->'job'->'assignments' ELSE '[]' END) recorded WHERE recorded->>'user_id'=u.id::text)) THEN
 RAISE EXCEPTION 'Job dispatch requires Administrator review: a new disabled assignee is not a recorded legacy assignment'; END IF;
 IF EXISTS(SELECT 1 FROM job_dispatch_legacy_approvals a CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(a.source->'job'->'assignments')='array' THEN a.source->'job'->'assignments' ELSE '[]' END) recorded
 JOIN users u ON u.id::text=recorded->>'user_id' WHERE NOT (a.plan->'assignee_ids') @> to_jsonb(ARRAY[u.id])) THEN
 RAISE EXCEPTION 'Job dispatch requires Administrator review: mappings must retain recorded legacy assignee IDs'; END IF;
END $$;
-- These are known, explicitly reviewed retained legacy relationships, not new
-- selections. Temporarily suspend only the new-person availability guard while
-- materializing them; company/identity checks above and the authority fence stay
-- in force. DDL rollback restores the guard on any migration failure.
ALTER TABLE job_assignments DISABLE TRIGGER directory_assignment_available;
INSERT INTO job_assignments(job_id,user_id,role)
 SELECT a.job_id,uid::bigint,'' FROM job_dispatch_legacy_approvals a CROSS JOIN LATERAL jsonb_array_elements_text(a.plan->'assignee_ids') uid
 WHERE NOT EXISTS(SELECT 1 FROM job_assignments j WHERE j.job_id=a.job_id AND j.user_id=uid::bigint);
ALTER TABLE job_assignments ENABLE TRIGGER directory_assignment_available;
-- Removing recorded assignments during upgrade is forbidden. Repair separately
-- after cutover with an ordinary reviewed dispatch action.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM job_assignments j JOIN job_dispatch_legacy_approvals a ON a.job_id=j.job_id WHERE NOT (a.plan->'assignee_ids') @> to_jsonb(ARRAY[j.user_id])) THEN
 RAISE EXCEPTION 'Job dispatch requires Administrator review: mappings must retain every recorded assignee'; END IF;
END $$;
-- Canonical due dates are local dates, not operational instants. Preserve the
-- original timestamptz in the inventory and capture the configured date once.
UPDATE jobs j SET due_date=((j.due_date AT TIME ZONE i.timezone)::date::timestamp AT TIME ZONE 'UTC')
 FROM job_dispatch_legacy_inventory i WHERE i.job_id=j.id AND j.due_date IS NOT NULL AND NOT EXISTS(SELECT 1 FROM job_dispatch_legacy_approvals a WHERE a.job_id=j.id);
ALTER TABLE jobs ALTER COLUMN due_date TYPE date USING (due_date AT TIME ZONE 'UTC')::date;
-- Refresh only the legacy read projection. Original JSON/labels remain in the
-- immutable source inventory and assignment relationship IDs are unchanged.
UPDATE jobs j SET assignments=normalized.value
 FROM (SELECT j.id,coalesce((SELECT jsonb_agg(jsonb_build_object('user_id',u.id,'name',u.name) ORDER BY u.id)
 FROM job_assignments a JOIN users u ON u.id=a.user_id WHERE a.job_id=j.id),'[]'::jsonb) value FROM jobs j) normalized
 WHERE normalized.id=j.id AND j.assignments IS DISTINCT FROM normalized.value;
CREATE FUNCTION job_dispatch_legacy_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'Dispatch legacy evidence is immutable'; END $$;
CREATE TRIGGER job_dispatch_inventory_immutable BEFORE UPDATE OR DELETE ON job_dispatch_legacy_inventory FOR EACH ROW EXECUTE FUNCTION job_dispatch_legacy_immutable();
CREATE TRIGGER job_dispatch_approval_immutable BEFORE UPDATE OR DELETE ON job_dispatch_legacy_approvals FOR EACH ROW EXECUTE FUNCTION job_dispatch_legacy_immutable();

-- Reviews are prospective user intent, never historical dispatch evidence.
CREATE TABLE job_dispatch_reviews (
 id text PRIMARY KEY,
 user_id bigint NOT NULL REFERENCES users(id),
 job_id bigint NOT NULL REFERENCES jobs(id),
 baseline text NOT NULL,
 proposal jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 committed_at timestamptz
);
