ALTER TABLE payment_method_labels ADD COLUMN revision UUID NOT NULL DEFAULT gen_random_uuid();
