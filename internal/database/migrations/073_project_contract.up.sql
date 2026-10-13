-- Preserve the original timestamps and recorded progress verbatim. Calendar
-- dates are captured once in the installation's configured display timezone;
-- future timezone changes never reinterpret these values.
CREATE TABLE project_legacy_review (
    project_id BIGINT PRIMARY KEY REFERENCES projects(id),
    recorded JSONB NOT NULL,
    captured_timezone TEXT,
    review_reasons TEXT[] NOT NULL,
    captured_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO project_legacy_review(project_id,recorded,captured_timezone,review_reasons)
SELECT p.id,to_jsonb(p),cs.timezone,array_remove(ARRAY[
 CASE WHEN p.company_id IS NULL OR c.company_id IS DISTINCT FROM p.company_id THEN 'ownership requires explicit review' END,
 CASE WHEN s.category_key='project:completed' AND p.completion_percentage=100 THEN 'prior non-completed manual progress is unknown; recorded 100 is not evidence' END,
 CASE WHEN p.completion_percentage<0 OR p.completion_percentage>100 OR p.completion_percentage<>trunc(p.completion_percentage) THEN 'recorded progress requires explicit integer review' END,
 CASE WHEN (p.start_time IS NOT NULL OR p.end_time IS NOT NULL) AND z.name IS NULL THEN 'planned dates require timezone review' END,
 CASE WHEN p.start_time>p.end_time THEN 'recorded dates are unordered' END
],NULL)
FROM projects p LEFT JOIN customers c ON c.id=p.customer_id
LEFT JOIN statuses s ON s.id=p.status_id
LEFT JOIN company_settings cs ON cs.company_id=p.company_id
LEFT JOIN pg_timezone_names z ON z.name=cs.timezone;

ALTER TABLE projects ADD COLUMN planned_start_date DATE;
ALTER TABLE projects ADD COLUMN planned_end_date DATE;
ALTER TABLE projects ADD COLUMN service_notes TEXT NOT NULL DEFAULT '';
UPDATE projects p SET
 planned_start_date=(p.start_time AT TIME ZONE r.captured_timezone)::date,
 planned_end_date=(p.end_time AT TIME ZONE r.captured_timezone)::date
FROM project_legacy_review r JOIN pg_timezone_names z ON z.name=r.captured_timezone WHERE r.project_id=p.id;

CREATE FUNCTION project_identity_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.customer_id IS DISTINCT FROM OLD.customer_id THEN
  RAISE EXCEPTION 'Project Customer ownership is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER project_identity_guard BEFORE UPDATE OF customer_id ON projects
FOR EACH ROW EXECUTE FUNCTION project_identity_guard();
