-- Stage 2: personal-library RAG plan persistence, brief feedback, and query indexes.
-- Idempotent: safe to re-apply and never rewrites Stage 0/1 data.

-- Persist the deterministic planner output on each turn so plans are auditable.
ALTER TABLE query_turns ADD COLUMN IF NOT EXISTS plan_json JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Brief feedback is append-only; it never mutates the immutable brief.
CREATE TABLE IF NOT EXISTS brief_feedback (
  id TEXT PRIMARY KEY,
  brief_id TEXT NOT NULL REFERENCES briefs(id),
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  actor_id TEXT NOT NULL REFERENCES actors(id),
  rating INTEGER NOT NULL,
  comment TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT brief_feedback_rating CHECK (rating BETWEEN -1 AND 1),
  CONSTRAINT brief_feedback_comment_len CHECK (char_length(comment) <= 2000)
);

CREATE INDEX IF NOT EXISTS brief_feedback_workspace_brief_idx
  ON brief_feedback(workspace_id, brief_id, created_at DESC);

-- Scope filtering joins document_collections by collection id.
CREATE INDEX IF NOT EXISTS document_collections_workspace_collection_doc_idx
  ON document_collections(workspace_id, collection_id, document_id);

-- Active, scoped retrieval over the current epoch version.
CREATE INDEX IF NOT EXISTS chunks_workspace_epoch_document_idx
  ON chunks(workspace_id, index_epoch_id, document_id, document_version_id);

-- Event stream reads are ordered by (session, seq).
CREATE INDEX IF NOT EXISTS query_events_workspace_session_seq_idx
  ON query_events(workspace_id, session_id, seq);

-- Scope snapshots and lifecycle filters on turns.
CREATE INDEX IF NOT EXISTS query_turns_workspace_session_status_idx
  ON query_turns(workspace_id, session_id, status);
