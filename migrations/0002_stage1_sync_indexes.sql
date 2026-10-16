-- Stage 1: Zotero Web API sync + index lifecycle indexes.
-- Idempotent: safe to re-apply and never rewrites Stage 0 data.

-- Ensure lifecycle columns exist on documents (Stage 0 created them already,
-- but the ADD COLUMN IF NOT EXISTS guard keeps this migration self-contained).
ALTER TABLE documents ADD COLUMN IF NOT EXISTS removed_at TIMESTAMPTZ;
ALTER TABLE documents ADD COLUMN IF NOT EXISTS purge_after TIMESTAMPTZ;
ALTER TABLE collections ADD COLUMN IF NOT EXISTS is_deleted BOOLEAN NOT NULL DEFAULT false;

-- Lifecycle / purge scans.
CREATE INDEX IF NOT EXISTS documents_workspace_active_idx
  ON documents(workspace_id, removed_at, purge_after);

-- Collection listing and soft-delete reconciliation scoped to a connection.
CREATE INDEX IF NOT EXISTS collections_connection_deleted_idx
  ON collections(connection_id, is_deleted);

-- Fast "latest version per document" lookups during idempotent sync.
CREATE INDEX IF NOT EXISTS document_versions_document_version_idx
  ON document_versions(document_id, version_no DESC);

-- Tombstone purge scanning within a workspace.
CREATE INDEX IF NOT EXISTS lifecycle_tombstones_workspace_status_purge_idx
  ON lifecycle_tombstones(workspace_id, object_type, status, purge_after);
