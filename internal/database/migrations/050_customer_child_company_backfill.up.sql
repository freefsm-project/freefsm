DO $$
BEGIN
  LOCK TABLE customers IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE projects IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE customer_contacts IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE assets IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE locations IN SHARE ROW EXCLUSIVE MODE;

  IF EXISTS(
    SELECT 1
    FROM projects child
    LEFT JOIN customers parent ON parent.id=child.customer_id
    WHERE parent.id IS NULL
       OR parent.company_id IS NULL
       OR (child.company_id IS NOT NULL AND child.company_id IS DISTINCT FROM parent.company_id)
  ) THEN
    RAISE EXCEPTION 'customer child ownership preflight: project parent is missing, unowned, or mismatched';
  END IF;

  IF EXISTS(
    SELECT 1
    FROM customer_contacts child
    LEFT JOIN customers parent ON parent.id=child.customer_id
    WHERE parent.id IS NULL
       OR parent.company_id IS NULL
       OR (child.company_id IS NOT NULL AND child.company_id IS DISTINCT FROM parent.company_id)
  ) THEN
    RAISE EXCEPTION 'customer child ownership preflight: contact parent is missing, unowned, or mismatched';
  END IF;

  IF EXISTS(
    SELECT 1
    FROM assets child
    LEFT JOIN customers parent ON parent.id=child.customer_id
    WHERE parent.id IS NULL
       OR parent.company_id IS NULL
       OR (child.company_id IS NOT NULL AND child.company_id IS DISTINCT FROM parent.company_id)
  ) THEN
    RAISE EXCEPTION 'customer child ownership preflight: asset parent is missing, unowned, or mismatched';
  END IF;

  IF EXISTS(
    SELECT 1
    FROM locations child
    LEFT JOIN customers parent ON parent.id=child.object_id
    WHERE child.object_type='customer'
      AND (parent.id IS NULL
        OR parent.company_id IS NULL
        OR (child.company_id IS NOT NULL AND child.company_id IS DISTINCT FROM parent.company_id))
  ) THEN
    RAISE EXCEPTION 'customer child ownership preflight: location parent is missing, unowned, or mismatched';
  END IF;

  UPDATE projects child
  SET company_id=parent.company_id
  FROM customers parent
  WHERE child.customer_id=parent.id AND child.company_id IS NULL;

  UPDATE customer_contacts child
  SET company_id=parent.company_id
  FROM customers parent
  WHERE child.customer_id=parent.id AND child.company_id IS NULL;

  UPDATE assets child
  SET company_id=parent.company_id
  FROM customers parent
  WHERE child.customer_id=parent.id AND child.company_id IS NULL;

  UPDATE locations child
  SET company_id=parent.company_id
  FROM customers parent
  WHERE child.object_type='customer' AND child.object_id=parent.id AND child.company_id IS NULL;

  IF EXISTS(
    SELECT 1 FROM projects child LEFT JOIN customers parent ON parent.id=child.customer_id
    WHERE parent.id IS NULL OR parent.company_id IS NULL OR child.company_id IS DISTINCT FROM parent.company_id
  ) OR EXISTS(
    SELECT 1 FROM customer_contacts child LEFT JOIN customers parent ON parent.id=child.customer_id
    WHERE parent.id IS NULL OR parent.company_id IS NULL OR child.company_id IS DISTINCT FROM parent.company_id
  ) OR EXISTS(
    SELECT 1 FROM assets child LEFT JOIN customers parent ON parent.id=child.customer_id
    WHERE parent.id IS NULL OR parent.company_id IS NULL OR child.company_id IS DISTINCT FROM parent.company_id
  ) OR EXISTS(
    SELECT 1 FROM locations child LEFT JOIN customers parent ON parent.id=child.object_id
    WHERE child.object_type='customer'
      AND (parent.id IS NULL OR parent.company_id IS NULL OR child.company_id IS DISTINCT FROM parent.company_id)
  ) THEN
    RAISE EXCEPTION 'customer child ownership recheck failed';
  END IF;
END $$;
