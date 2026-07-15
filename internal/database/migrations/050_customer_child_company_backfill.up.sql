UPDATE projects child
SET company_id=parent.company_id
FROM customers parent
WHERE child.customer_id=parent.id
  AND child.company_id IS NULL
  AND parent.company_id IS NOT NULL;

UPDATE customer_contacts child
SET company_id=parent.company_id
FROM customers parent
WHERE child.customer_id=parent.id
  AND child.company_id IS NULL
  AND parent.company_id IS NOT NULL;

UPDATE assets child
SET company_id=parent.company_id
FROM customers parent
WHERE child.customer_id=parent.id
  AND child.company_id IS NULL
  AND parent.company_id IS NOT NULL;

UPDATE locations child
SET company_id=parent.company_id
FROM customers parent
WHERE child.object_type='customer'
  AND child.object_id=parent.id
  AND child.company_id IS NULL
  AND parent.company_id IS NOT NULL;
