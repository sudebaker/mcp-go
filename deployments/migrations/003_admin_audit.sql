-- Migration 003: Admin audit log and KB performance index

CREATE TABLE IF NOT EXISTS admin_audit_log (
    id BIGSERIAL PRIMARY KEY,
    admin_action VARCHAR(50) NOT NULL,
    target_user_id VARCHAR(255),
    target_collection VARCHAR(255),
    docs_deleted INTEGER,
    bytes_freed BIGINT,
    request_id VARCHAR(255),
    created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_admin_audit_created_at ON admin_audit_log(created_at DESC);

CREATE INDEX IF NOT EXISTS idx_kb_documents_user_collection
ON kb_documents(user_id, collection);
