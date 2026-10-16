CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS schema_migrations (
  version TEXT PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS actors (
  id TEXT PRIMARY KEY,
  external_subject TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS workspaces (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS workspace_members (
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  actor_id TEXT NOT NULL REFERENCES actors(id),
  role TEXT NOT NULL DEFAULT 'user',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, actor_id),
  CONSTRAINT workspace_members_role CHECK (role IN ('user','admin'))
);

CREATE TABLE IF NOT EXISTS zotero_connections (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  actor_id TEXT NOT NULL REFERENCES actors(id),
  provider TEXT NOT NULL DEFAULT 'web_api',
  library_type TEXT NOT NULL DEFAULT 'user',
  library_id TEXT NOT NULL,
  encrypted_credential TEXT,
  credential_key_version TEXT,
  status TEXT NOT NULL DEFAULT 'healthy',
  last_library_version BIGINT NOT NULL DEFAULT 0,
  last_sync_at TIMESTAMPTZ,
  last_success_at TIMESTAMPTZ,
  coverage_jsonb JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at TIMESTAMPTZ,
  purge_at TIMESTAMPTZ,
  UNIQUE (workspace_id, provider, library_id),
  CONSTRAINT zotero_connections_status CHECK (status IN ('healthy','degraded','disconnected','revoked'))
);

CREATE TABLE IF NOT EXISTS zotero_sync_runs (
  id TEXT PRIMARY KEY,
  connection_id TEXT NOT NULL REFERENCES zotero_connections(id),
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  mode TEXT NOT NULL,
  status TEXT NOT NULL,
  from_version BIGINT NOT NULL DEFAULT 0,
  to_version BIGINT,
  cursor_jsonb JSONB NOT NULL DEFAULT '{}',
  counters_jsonb JSONB NOT NULL DEFAULT '{}',
  error_code TEXT,
  error_message TEXT,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at TIMESTAMPTZ,
  CONSTRAINT zotero_sync_runs_mode CHECK (mode IN ('full','incremental','retry')),
  CONSTRAINT zotero_sync_runs_status CHECK (status IN ('pending','running','partial','complete','failed','canceled'))
);

CREATE TABLE IF NOT EXISTS collections (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  connection_id TEXT NOT NULL REFERENCES zotero_connections(id),
  zotero_key TEXT NOT NULL,
  parent_key TEXT,
  name TEXT NOT NULL,
  version BIGINT NOT NULL DEFAULT 0,
  is_deleted BOOLEAN NOT NULL DEFAULT false,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (connection_id, zotero_key)
);

CREATE TABLE IF NOT EXISTS documents (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  connection_id TEXT NOT NULL REFERENCES zotero_connections(id),
  zotero_item_key TEXT NOT NULL,
  item_type TEXT NOT NULL DEFAULT 'journalArticle',
  title TEXT NOT NULL,
  abstract TEXT NOT NULL DEFAULT '',
  year INTEGER,
  venue TEXT NOT NULL DEFAULT '',
  doi_normalized TEXT NOT NULL DEFAULT '',
  authors_json JSONB NOT NULL DEFAULT '[]',
  identifiers_json JSONB NOT NULL DEFAULT '{}',
  active_version_id TEXT,
  index_status TEXT NOT NULL DEFAULT 'pending',
  evidence_type TEXT,
  zotero_version BIGINT NOT NULL DEFAULT 0,
  content_hash TEXT NOT NULL DEFAULT '',
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  removed_at TIMESTAMPTZ,
  purge_after TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (connection_id, zotero_item_key),
  CONSTRAINT documents_index_status CHECK (index_status IN ('pending','indexed','metadata_only','failed','skipped')),
  CONSTRAINT documents_evidence_type CHECK (evidence_type IS NULL OR evidence_type IN ('personal_fulltext','structured_abstract'))
);

CREATE TABLE IF NOT EXISTS document_versions (
  id TEXT PRIMARY KEY,
  document_id TEXT NOT NULL REFERENCES documents(id),
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  version_no INTEGER NOT NULL,
  source_kind TEXT NOT NULL,
  fulltext TEXT NOT NULL DEFAULT '',
  abstract TEXT NOT NULL DEFAULT '',
  content_hash TEXT NOT NULL,
  published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (document_id, version_no),
  CONSTRAINT document_versions_source_kind CHECK (source_kind IN ('fulltext','abstract','metadata'))
);

CREATE TABLE IF NOT EXISTS document_collections (
  document_id TEXT NOT NULL REFERENCES documents(id),
  collection_id TEXT NOT NULL REFERENCES collections(id),
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  PRIMARY KEY (document_id, collection_id)
);

CREATE TABLE IF NOT EXISTS index_epochs (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  status TEXT NOT NULL DEFAULT 'building',
  source_sync_run_id TEXT REFERENCES zotero_sync_runs(id),
  embedding_version TEXT NOT NULL,
  retrieval_config_version TEXT NOT NULL,
  content_hash TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  published_at TIMESTAMPTZ,
  retired_at TIMESTAMPTZ,
  CONSTRAINT index_epochs_status CHECK (status IN ('building','active','retired'))
);

CREATE UNIQUE INDEX IF NOT EXISTS index_epochs_one_active ON index_epochs(workspace_id) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS index_epoch_documents (
  epoch_id TEXT NOT NULL REFERENCES index_epochs(id),
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  document_id TEXT NOT NULL REFERENCES documents(id),
  document_version_id TEXT NOT NULL REFERENCES document_versions(id),
  included_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (epoch_id, document_id)
);

CREATE TABLE IF NOT EXISTS chunks (
  id TEXT PRIMARY KEY,
  index_epoch_id TEXT NOT NULL REFERENCES index_epochs(id),
  document_version_id TEXT NOT NULL REFERENCES document_versions(id),
  document_id TEXT NOT NULL REFERENCES documents(id),
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  ordinal INTEGER NOT NULL,
  section TEXT NOT NULL DEFAULT '',
  page_start INTEGER,
  page_end INTEGER,
  char_start INTEGER NOT NULL DEFAULT 0,
  char_end INTEGER NOT NULL DEFAULT 0,
  content TEXT NOT NULL,
  token_count INTEGER NOT NULL DEFAULT 0,
  search_tsv TSVECTOR NOT NULL DEFAULT ''::tsvector,
  embedding vector(1024),
  embedding_version TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (index_epoch_id, document_version_id, ordinal)
);

CREATE INDEX IF NOT EXISTS chunks_epoch_idx ON chunks(index_epoch_id, document_id);
CREATE INDEX IF NOT EXISTS chunks_search_idx ON chunks USING GIN(search_tsv);
CREATE INDEX IF NOT EXISTS chunks_embedding_idx ON chunks USING hnsw (embedding vector_cosine_ops);

CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  default_mode TEXT NOT NULL DEFAULT 'my_library',
  scope_mode TEXT NOT NULL DEFAULT 'all_library',
  collection_ids_json JSONB NOT NULL DEFAULT '[]',
  scope_version INTEGER NOT NULL DEFAULT 1,
  deleted_at TIMESTAMPTZ,
  purge_after TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT projects_default_mode CHECK (default_mode IN ('my_library','gap_fill','landscape')),
  CONSTRAINT projects_scope_mode CHECK (scope_mode IN ('all_library','selected_collections'))
);

CREATE TABLE IF NOT EXISTS query_sessions (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  project_id TEXT NOT NULL REFERENCES projects(id),
  title TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'accepted',
  index_epoch_id TEXT NOT NULL REFERENCES index_epochs(id),
  deleted_at TIMESTAMPTZ,
  purge_after TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS query_turns (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES query_sessions(id),
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  turn_no INTEGER NOT NULL,
  question TEXT NOT NULL,
  mode TEXT NOT NULL,
  scope_snapshot_json JSONB NOT NULL DEFAULT '{}',
  index_epoch_id TEXT NOT NULL REFERENCES index_epochs(id),
  planner_version TEXT NOT NULL,
  retrieval_config_version TEXT NOT NULL,
  prompt_version TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'accepted',
  phase TEXT NOT NULL DEFAULT 'accepted',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ,
  UNIQUE (session_id, turn_no),
  CONSTRAINT query_turns_mode CHECK (mode IN ('my_library','gap_fill','landscape'))
);

CREATE TABLE IF NOT EXISTS query_events (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES query_sessions(id),
  turn_id TEXT REFERENCES query_turns(id),
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  seq BIGINT NOT NULL,
  type TEXT NOT NULL,
  data_json JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (session_id, seq)
);

CREATE TABLE IF NOT EXISTS briefs (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES query_sessions(id),
  turn_id TEXT NOT NULL UNIQUE REFERENCES query_turns(id),
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  schema_version TEXT NOT NULL DEFAULT '1.1',
  summary TEXT NOT NULL DEFAULT '',
  findings_json JSONB NOT NULL DEFAULT '[]',
  method_timeline_json JSONB NOT NULL DEFAULT '[]',
  chronology_json JSONB NOT NULL DEFAULT '[]',
  open_questions_json JSONB NOT NULL DEFAULT '[]',
  source_status_json JSONB NOT NULL DEFAULT '{}',
  index_epoch_id TEXT NOT NULL REFERENCES index_epochs(id),
  retrieval_config_version TEXT NOT NULL,
  prompt_version TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS evidence (
  id TEXT PRIMARY KEY,
  brief_id TEXT NOT NULL REFERENCES briefs(id),
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  index_epoch_id TEXT NOT NULL REFERENCES index_epochs(id),
  evidence_type TEXT NOT NULL,
  document_id TEXT,
  document_version_id TEXT,
  chunk_id TEXT,
  external_work_id TEXT,
  title TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  quote TEXT NOT NULL DEFAULT '',
  locator_json JSONB NOT NULL DEFAULT '{}',
  score DOUBLE PRECISION NOT NULL DEFAULT 0,
  retrieved_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  source_meta_json JSONB NOT NULL DEFAULT '{}',
  CONSTRAINT evidence_type CHECK (evidence_type IN ('personal_fulltext','structured_abstract','web_context'))
);

CREATE TABLE IF NOT EXISTS claims (
  id TEXT PRIMARY KEY,
  brief_id TEXT NOT NULL REFERENCES briefs(id),
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  claim_no INTEGER NOT NULL,
  claim_type TEXT NOT NULL,
  text TEXT NOT NULL,
  support_level TEXT NOT NULL,
  confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
  verification_status TEXT NOT NULL DEFAULT 'pending',
  constraints_json JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (brief_id, claim_type, claim_no),
  CONSTRAINT claims_type CHECK (claim_type IN ('summary','finding','method','chronology')),
  CONSTRAINT claims_verification_status CHECK (verification_status IN ('pending','verified','partial','rejected'))
);

CREATE TABLE IF NOT EXISTS claim_evidence (
  claim_id TEXT NOT NULL REFERENCES claims(id) ON DELETE CASCADE,
  evidence_id TEXT NOT NULL REFERENCES evidence(id) ON DELETE CASCADE,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  stance TEXT NOT NULL DEFAULT 'supports',
  support_score DOUBLE PRECISION NOT NULL DEFAULT 0,
  evidence_span TEXT NOT NULL DEFAULT '',
  locator_json JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (claim_id, evidence_id, stance),
  CONSTRAINT claim_evidence_stance CHECK (stance IN ('supports','refutes','background'))
);

CREATE TABLE IF NOT EXISTS gaps (
  id TEXT PRIMARY KEY,
  brief_id TEXT NOT NULL REFERENCES briefs(id),
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  gap_type TEXT NOT NULL,
  missing_relative_to TEXT NOT NULL,
  explanation TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT gaps_type CHECK (gap_type IN ('canonical','related_method','latest_progress')),
  CONSTRAINT gaps_missing_relative_to CHECK (missing_relative_to IN ('personal_library','current_scope','external_only'))
);

CREATE TABLE IF NOT EXISTS external_works (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  identity_key TEXT NOT NULL,
  doi_normalized TEXT NOT NULL DEFAULT '',
  s2_id TEXT NOT NULL DEFAULT '',
  openalex_id TEXT NOT NULL DEFAULT '',
  pmid TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL,
  abstract TEXT NOT NULL DEFAULT '',
  year INTEGER,
  venue TEXT NOT NULL DEFAULT '',
  citation_count INTEGER NOT NULL DEFAULT 0,
  type TEXT NOT NULL DEFAULT '',
  source_json JSONB NOT NULL DEFAULT '{}',
  first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (workspace_id, identity_key)
);

CREATE TABLE IF NOT EXISTS audit_events (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  actor_id TEXT NOT NULL REFERENCES actors(id),
  action TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  request_id TEXT NOT NULL DEFAULT '',
  metadata_json JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS lifecycle_tombstones (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  object_type TEXT NOT NULL,
  object_id TEXT NOT NULL,
  reason TEXT NOT NULL,
  effective_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  purge_after TIMESTAMPTZ NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  last_error TEXT NOT NULL DEFAULT '',
  completed_at TIMESTAMPTZ,
  CONSTRAINT lifecycle_tombstones_object_type CHECK (object_type IN ('zotero_connection','document','project','query_session','account')),
  CONSTRAINT lifecycle_tombstones_status CHECK (status IN ('active','purging','purged','retry'))
);

CREATE INDEX IF NOT EXISTS zotero_connections_workspace_status_idx ON zotero_connections(workspace_id, status);
CREATE INDEX IF NOT EXISTS zotero_sync_runs_connection_started_idx ON zotero_sync_runs(connection_id, started_at DESC);
CREATE INDEX IF NOT EXISTS collections_connection_parent_idx ON collections(connection_id, parent_key);
CREATE INDEX IF NOT EXISTS documents_workspace_status_idx ON documents(workspace_id, index_status);
CREATE INDEX IF NOT EXISTS documents_workspace_doi_idx ON documents(workspace_id, doi_normalized);
CREATE INDEX IF NOT EXISTS document_collections_collection_idx ON document_collections(collection_id, document_id);
CREATE INDEX IF NOT EXISTS index_epochs_workspace_status_idx ON index_epochs(workspace_id, status);
CREATE INDEX IF NOT EXISTS query_sessions_workspace_created_idx ON query_sessions(workspace_id, deleted_at, created_at DESC);
CREATE INDEX IF NOT EXISTS query_events_session_seq_idx ON query_events(session_id, seq);
CREATE INDEX IF NOT EXISTS claims_brief_type_idx ON claims(brief_id, claim_type, claim_no);
CREATE INDEX IF NOT EXISTS claim_evidence_claim_idx ON claim_evidence(claim_id, evidence_id);
CREATE INDEX IF NOT EXISTS evidence_brief_type_score_idx ON evidence(brief_id, evidence_type, score DESC);
CREATE INDEX IF NOT EXISTS gaps_brief_type_idx ON gaps(brief_id, gap_type);
CREATE INDEX IF NOT EXISTS lifecycle_tombstones_status_purge_idx ON lifecycle_tombstones(status, purge_after);
