LOCK TABLE companies IN SHARE MODE;
LOCK TABLE customers, projects, customer_contacts, assets, locations, jobs, estimates, invoices, items, time_entries, company_settings IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
  company_count BIGINT;
  sole_company_id BIGINT;
  conflicting_invoice_number BIGINT;
BEGIN
  SELECT count(*) INTO company_count FROM companies;

  IF company_count > 1 AND (
    EXISTS (SELECT 1 FROM customers WHERE company_id IS NULL) OR
    EXISTS (SELECT 1 FROM jobs WHERE company_id IS NULL) OR
    EXISTS (SELECT 1 FROM estimates WHERE company_id IS NULL) OR
    EXISTS (SELECT 1 FROM invoices WHERE company_id IS NULL) OR
    EXISTS (SELECT 1 FROM items WHERE company_id IS NULL) OR
    EXISTS (SELECT 1 FROM time_entries WHERE company_id IS NULL)
  ) THEN
    RAISE EXCEPTION 'ambiguous tenant ownership with % companies', company_count;
  END IF;

  IF company_count = 1 THEN
    SELECT id INTO sole_company_id FROM companies;

    SELECT unowned.invoice_number INTO conflicting_invoice_number
    FROM invoices unowned
    JOIN invoices owned
      ON owned.company_id=sole_company_id
      AND owned.invoice_number=unowned.invoice_number
    WHERE unowned.company_id IS NULL
    ORDER BY unowned.invoice_number
    LIMIT 1;
    IF conflicting_invoice_number IS NOT NULL THEN
      RAISE EXCEPTION 'invoice number collision for sole company %: invoice_number % is both companyless and already owned', sole_company_id, conflicting_invoice_number;
    END IF;

    UPDATE customers
    SET company_id=sole_company_id
    WHERE company_id IS NULL;

    UPDATE jobs
    SET company_id=sole_company_id
    WHERE company_id IS NULL;

    UPDATE estimates
    SET company_id=sole_company_id
    WHERE company_id IS NULL;

    UPDATE invoices
    SET company_id=sole_company_id
    WHERE company_id IS NULL;

    UPDATE company_settings settings
    SET next_invoice_number=GREATEST(
      settings.next_invoice_number,
      COALESCE((SELECT max(invoice_number)+1 FROM invoices WHERE company_id=sole_company_id), 1)
    )
    WHERE settings.company_id=sole_company_id;

    UPDATE items
    SET company_id=sole_company_id
    WHERE company_id IS NULL;

    UPDATE time_entries
    SET company_id=sole_company_id
    WHERE company_id IS NULL;
  END IF;
END $$;

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
