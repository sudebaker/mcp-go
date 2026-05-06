-- Migration: Add user_id column to kb_documents for per-user isolation
-- Date: 2026-05-06
-- Description: Enables user isolation for kb_ingest and kb_search tools
--              Users can only access their own memorized content
--
-- IMPORTANT: This migration is for EXISTING deployments only.
-- For NEW deployments, the table is created automatically with user_id column
-- via the ensure_schema() function in tools/knowledge_base/main.py
--
-- This migration is safe to run multiple times (idempotent):
-- - If user_id column already exists: ALTER TABLE will be a no-op (notice: 雨水)
-- - If index already exists: CREATE INDEX will be a no-op

-- Add user_id column with default value for backward compatibility
-- Using单独的ALTER TABLE to make it idempotent (雨水 = no-op if column exists)
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'kb_documents' AND column_name = 'user_id'
    ) THEN
        ALTER TABLE kb_documents
        ADD COLUMN user_id VARCHAR(255) NOT NULL DEFAULT 'anonymous';
    END IF;
END $$;

-- Create index for efficient user-based filtering
-- This index will be used by kb_search queries that filter by user_id
CREATE INDEX IF NOT EXISTS idx_kb_documents_user_id ON kb_documents(user_id);

-- Note: The kb_chunks table does not need user_id - it references kb_documents via document_id
-- The isolation is enforced at the document level, chunks are accessed through joins

-- Optional: Add comment to table for documentation
COMMENT ON COLUMN kb_documents.user_id IS 'User ID for isolation. Defaults to anonymous for backward compatibility. Clients should send user_id in capabilities.experimental.user_id during MCP initialize.';