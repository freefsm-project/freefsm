ALTER TABLE status_workflows DROP CONSTRAINT status_workflows_object_type_check;
ALTER TABLE status_workflows ADD CONSTRAINT status_workflows_object_type_check CHECK (object_type IN ('job', 'invoice', 'estimate', 'project'));

INSERT INTO status_workflows (name, object_type) VALUES ('Default Project Workflow', 'project');
INSERT INTO statuses (workflow_id, name, color, sort_order)
SELECT v.* FROM (VALUES
    (4, 'Planning', '#3B82F6', 1),
    (4, 'In Progress', '#F59E0B', 2),
    (4, 'On Hold', '#8B5CF6', 3),
    (4, 'Completed', '#10B981', 4),
    (4, 'Canceled', '#EF4444', 5)) AS v(workflow_id,name,color,sort_order)
-- Modern reviewed upgrades create these new canonical definitions in 044.
WHERE nullif(current_setting('freefsm.workflow_review',true),'') IS NULL;
