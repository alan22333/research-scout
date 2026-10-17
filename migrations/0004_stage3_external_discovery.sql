-- Stage 3: external discovery, research gaps and web-context provenance.
-- Idempotent: only additive columns, indexes and new tables; Stage 0-2 data is
-- never rewritten.

-- external_works was created in Stage 0 as an empty placeholder. Stage 3 adds
-- the fields needed to persist a normalized provider-agnostic record.
ALTER TABLE external_works ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'available';
ALTER TABLE external_works ADD COLUMN IF NOT EXISTS sources_json JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE external_works ADD COLUMN IF NOT EXISTS authors_json JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE external_works ADD COLUMN IF NOT EXISTS retrieved_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Gaps gain a machine-readable rationale and the external work they reference.
ALTER TABLE gaps ADD COLUMN IF NOT EXISTS external_work_id TEXT NOT NULL DEFAULT '';
ALTER TABLE gaps ADD COLUMN IF NOT EXISTS rationale_json JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE gaps ADD COLUMN IF NOT EXISTS score DOUBLE PRECISION NOT NULL DEFAULT 0;

-- Read-only citation/reference passthrough. Edges are workspace scoped and
-- never expanded into a second discovery hop in Stage 3.
CREATE TABLE IF NOT EXISTS external_citation_edges (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  external_work_id TEXT NOT NULL REFERENCES external_works(id),
  direction TEXT NOT NULL,
  related_identity_key TEXT NOT NULL,
  related_doi TEXT NOT NULL DEFAULT '',
  related_title TEXT NOT NULL DEFAULT '',
  related_year INTEGER NOT NULL DEFAULT 0,
  provider TEXT NOT NULL DEFAULT '',
  retrieved_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT external_citation_edges_direction CHECK (direction IN ('cites','cited_by')),
  UNIQUE (workspace_id, external_work_id, direction, related_identity_key)
);

-- Deduplication and discovery reads are always workspace scoped.
CREATE INDEX IF NOT EXISTS external_works_workspace_identity_idx
  ON external_works(workspace_id, identity_key);
CREATE INDEX IF NOT EXISTS external_works_workspace_doi_idx
  ON external_works(workspace_id, doi_normalized) WHERE doi_normalized <> '';
CREATE INDEX IF NOT EXISTS external_works_workspace_year_idx
  ON external_works(workspace_id, year DESC);
CREATE INDEX IF NOT EXISTS external_citation_edges_workspace_work_idx
  ON external_citation_edges(workspace_id, external_work_id, direction);
CREATE INDEX IF NOT EXISTS gaps_workspace_external_work_idx
  ON gaps(workspace_id, external_work_id) WHERE external_work_id <> '';
