DELETE FROM statuses
WHERE name = 'Invoiced'
  AND workflow_id IN (SELECT id FROM status_workflows WHERE object_type = 'invoice');

UPDATE statuses
SET sort_order = CASE name
    WHEN 'Draft' THEN 1
    WHEN 'Sent' THEN 2
    WHEN 'Paid' THEN 3
    WHEN 'Partially Paid' THEN 4
    WHEN 'Void' THEN 5
    ELSE sort_order
END
WHERE workflow_id IN (SELECT id FROM status_workflows WHERE object_type = 'invoice')
  AND name IN ('Draft', 'Sent', 'Paid', 'Partially Paid', 'Void');
