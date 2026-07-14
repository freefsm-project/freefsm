CREATE INDEX activity_logs_company_created_id_idx
    ON activity_logs(company_id, created_at DESC, id DESC);

CREATE INDEX activity_logs_company_type_created_id_idx
    ON activity_logs(company_id, object_type, created_at DESC, id DESC);

CREATE INDEX activity_logs_company_object_created_id_idx
    ON activity_logs(company_id, object_type, object_id, created_at DESC, id DESC);
