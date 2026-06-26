INSERT INTO statuses (workflow_id, name, color, sort_order)
SELECT sw.id, 'Invoiced', '#7C3AED', 2
FROM status_workflows sw
WHERE sw.object_type = 'invoice'
  AND NOT EXISTS (
    SELECT 1
    FROM statuses s
    WHERE s.workflow_id = sw.id
      AND LOWER(s.name) = 'invoiced'
  );

UPDATE statuses
SET sort_order = CASE name
    WHEN 'Draft' THEN 1
    WHEN 'Invoiced' THEN 2
    WHEN 'Sent' THEN 3
    WHEN 'Partially Paid' THEN 4
    WHEN 'Paid' THEN 5
    WHEN 'Void' THEN 6
    ELSE sort_order
END
WHERE workflow_id IN (SELECT id FROM status_workflows WHERE object_type = 'invoice')
  AND name IN ('Draft', 'Invoiced', 'Sent', 'Partially Paid', 'Paid', 'Void');
