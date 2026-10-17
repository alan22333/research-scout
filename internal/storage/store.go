package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, fmt.Errorf("database URL is required")
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	// Stage 0 migrations contain multiple SQL statements in one file. The simple
	// protocol lets a single Exec execute them without relying on a migration CLI.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("store is not initialized")
	}
	return s.pool.Ping(ctx)
}

func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}

func StableID(namespace string, parts ...string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(namespace))
	for _, part := range parts {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(part))
	}
	return namespace + "-" + hex.EncodeToString(h.Sum(nil))[:24]
}

func JSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func SeedLocal(ctx context.Context, s *Store, actorID, workspaceID string) error {
	if actorID == "" || workspaceID == "" {
		return fmt.Errorf("actor_id and workspace_id are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO actors (id, external_subject, display_name)
		VALUES ($1, $2, 'Local User')
		ON CONFLICT (id) DO UPDATE SET external_subject = EXCLUDED.external_subject,
			display_name = EXCLUDED.display_name
	`, actorID, "local:"+actorID); err != nil {
		return fmt.Errorf("seed actor: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO workspaces (id, name)
		VALUES ($1, 'Local Workspace')
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name
	`, workspaceID); err != nil {
		return fmt.Errorf("seed workspace: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO workspace_members (workspace_id, actor_id, role)
		VALUES ($1, $2, 'user')
		ON CONFLICT (workspace_id, actor_id) DO NOTHING
	`, workspaceID, actorID); err != nil {
		return fmt.Errorf("seed workspace member: %w", err)
	}
	connectionID := StableID("conn", workspaceID, "web_api", "local-fixture")
	if _, err := tx.Exec(ctx, `
		INSERT INTO zotero_connections
			(id, workspace_id, actor_id, provider, library_type, library_id, status, coverage_jsonb)
		VALUES ($1, $2, $3, 'web_api', 'user', 'local-fixture', 'healthy',
			'{"fixture":true,"local_only_warning":true}'::jsonb)
		ON CONFLICT (id) DO UPDATE SET status = 'healthy', revoked_at = NULL, purge_at = NULL,
			updated_at = now()
	`, connectionID, workspaceID, actorID); err != nil {
		return fmt.Errorf("seed zotero connection: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Store) HasActiveEpoch(ctx context.Context, workspaceID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM index_epochs
			WHERE workspace_id = $1 AND status = 'active' AND retired_at IS NULL
		)
	`, workspaceID).Scan(&exists)
	return exists, err
}

type EpochMetadata struct {
	ID                     string
	EmbeddingVersion       string
	RetrievalConfigVersion string
}

func (s *Store) ActiveEpochMetadata(ctx context.Context, workspaceID string) (EpochMetadata, error) {
	var out EpochMetadata
	err := s.pool.QueryRow(ctx, `
		SELECT id, embedding_version, retrieval_config_version
		FROM index_epochs
		WHERE workspace_id = $1 AND status = 'active' AND retired_at IS NULL
		ORDER BY published_at DESC NULLS LAST
		LIMIT 1
	`, workspaceID).Scan(&out.ID, &out.EmbeddingVersion, &out.RetrievalConfigVersion)
	if err != nil {
		return EpochMetadata{}, err
	}
	return out, nil
}

func (s *Store) ActiveEpochID(ctx context.Context, workspaceID string) (string, error) {
	epoch, err := s.ActiveEpochMetadata(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	return epoch.ID, nil
}

type Project struct {
	ID            string    `json:"id"`
	WorkspaceID   string    `json:"workspace_id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	DefaultMode   string    `json:"default_mode"`
	ScopeMode     string    `json:"scope_mode"`
	CollectionIDs []string  `json:"collection_ids"`
	ScopeVersion  int       `json:"scope_version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Session struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	Phase        string    `json:"phase"`
	IndexEpochID string    `json:"index_epoch_id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Turns        []Turn    `json:"turns,omitempty"`
}

type Turn struct {
	ID                     string    `json:"id"`
	SessionID              string    `json:"session_id"`
	TurnNo                 int       `json:"turn_no"`
	Question               string    `json:"question"`
	Mode                   string    `json:"mode"`
	Status                 string    `json:"status"`
	Phase                  string    `json:"phase"`
	IndexEpochID           string    `json:"index_epoch_id"`
	PlannerVersion         string    `json:"planner_version"`
	RetrievalConfigVersion string    `json:"retrieval_config_version"`
	PromptVersion          string    `json:"prompt_version"`
	CreatedAt              time.Time `json:"created_at"`
}

func (s *Store) CreateProject(ctx context.Context, p Project) (Project, error) {
	if p.ID == "" {
		p.ID = StableID("project", p.WorkspaceID, p.Name, fmt.Sprint(time.Now().UnixNano()))
	}
	if p.ScopeMode == "" {
		p.ScopeMode = "all_library"
	}
	if p.DefaultMode == "" {
		p.DefaultMode = "my_library"
	}
	if p.CollectionIDs == nil {
		p.CollectionIDs = []string{}
	}
	scopeJSON, err := JSON(p.CollectionIDs)
	if err != nil {
		return Project{}, err
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO projects
			(id, workspace_id, name, description, default_mode, scope_mode, collection_ids_json, scope_version)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,1)
		ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, description=EXCLUDED.description,
			default_mode=EXCLUDED.default_mode, scope_mode=EXCLUDED.scope_mode,
			collection_ids_json=EXCLUDED.collection_ids_json, updated_at=now()
		RETURNING scope_version, created_at, updated_at
	`, p.ID, p.WorkspaceID, p.Name, p.Description, p.DefaultMode, p.ScopeMode, scopeJSON).Scan(&p.ScopeVersion, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return Project{}, err
	}
	return p, nil
}

func (s *Store) GetProject(ctx context.Context, workspaceID, projectID string) (Project, error) {
	var p Project
	var scopeRaw []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, workspace_id, name, description, default_mode, scope_mode,
			collection_ids_json, scope_version, created_at, updated_at
		FROM projects
		WHERE workspace_id=$1 AND id=$2 AND deleted_at IS NULL
	`, workspaceID, projectID).Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Description, &p.DefaultMode, &p.ScopeMode, &scopeRaw, &p.ScopeVersion, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return Project{}, err
	}
	if err := json.Unmarshal(scopeRaw, &p.CollectionIDs); err != nil {
		return Project{}, err
	}
	return p, nil
}

func (s *Store) ListProjects(ctx context.Context, workspaceID string) ([]Project, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workspace_id, name, description, default_mode, scope_mode,
			collection_ids_json, scope_version, created_at, updated_at
		FROM projects
		WHERE workspace_id=$1 AND deleted_at IS NULL
		ORDER BY created_at DESC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		var scopeRaw []byte
		if err := rows.Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Description, &p.DefaultMode, &p.ScopeMode, &scopeRaw, &p.ScopeVersion, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(scopeRaw, &p.CollectionIDs)
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProjectScope validates and persists a project's default scope. A
// selected scope must be non-empty and reference collections that belong to the
// workspace and are not deleted. all_library clears collection ids. Validation
// failures never touch the database.
func (s *Store) UpdateProjectScope(ctx context.Context, workspaceID, projectID, scopeMode string, collectionIDs []string) (Project, error) {
	if scopeMode != "all_library" && scopeMode != "selected_collections" {
		return Project{}, fmt.Errorf("invalid scope_mode")
	}
	switch scopeMode {
	case "all_library":
		collectionIDs = []string{}
	case "selected_collections":
		ids, err := normalizeCollectionIDs(collectionIDs)
		if err != nil {
			return Project{}, err
		}
		if len(ids) == 0 {
			return Project{}, ErrScopeEmpty
		}
		visible, err := s.visibleCollectionIDs(ctx, workspaceID, ids)
		if err != nil {
			return Project{}, err
		}
		for _, id := range ids {
			if !visible[id] {
				return Project{}, ErrForbidden
			}
		}
		collectionIDs = ids
	}
	scopeJSON, err := JSON(collectionIDs)
	if err != nil {
		return Project{}, err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE projects
		SET scope_mode=$3, collection_ids_json=$4::jsonb, scope_version=scope_version+1, updated_at=now()
		WHERE workspace_id=$1 AND id=$2 AND deleted_at IS NULL
	`, workspaceID, projectID, scopeMode, scopeJSON)
	if err != nil {
		return Project{}, err
	}
	if tag.RowsAffected() == 0 {
		return Project{}, pgx.ErrNoRows
	}
	return s.GetProject(ctx, workspaceID, projectID)
}

func normalizeCollectionIDs(ids []string) ([]string, error) {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, fmt.Errorf("collection id must not be empty")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// visibleCollectionIDs returns the subset of ids that belong to the workspace
// and are not soft-deleted.
func (s *Store) visibleCollectionIDs(ctx context.Context, workspaceID string, ids []string) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM collections
		WHERE workspace_id=$1 AND is_deleted=false AND id = ANY($2)
	`, workspaceID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// DeleteProject soft-deletes a project and starts its 30-day purge countdown.
func (s *Store) DeleteProject(ctx context.Context, workspaceID, projectID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE projects
		SET deleted_at=now(), purge_after=now() + interval '30 days', updated_at=now()
		WHERE workspace_id=$1 AND id=$2 AND deleted_at IS NULL
	`, workspaceID, projectID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// DeleteSession soft-deletes a query session and its turns visibility countdown.
func (s *Store) DeleteSession(ctx context.Context, workspaceID, sessionID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE query_sessions
		SET deleted_at=now(), purge_after=now() + interval '30 days', updated_at=now()
		WHERE workspace_id=$1 AND id=$2 AND deleted_at IS NULL
	`, workspaceID, sessionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// CancelSession marks a running session and its active turn as canceled.
func (s *Store) CancelSession(ctx context.Context, workspaceID, sessionID string) (Session, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE query_sessions
		SET status='canceled', updated_at=now()
		WHERE workspace_id=$1 AND id=$2 AND deleted_at IS NULL
	`, workspaceID, sessionID)
	if err != nil {
		return Session{}, err
	}
	if tag.RowsAffected() == 0 {
		return Session{}, pgx.ErrNoRows
	}
	if _, err := tx.Exec(ctx, `
		UPDATE query_turns
		SET status='canceled', phase='canceled', completed_at=now()
		WHERE workspace_id=$1 AND session_id=$2 AND status IN ('accepted','running')
	`, workspaceID, sessionID); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, err
	}
	return s.GetSession(ctx, workspaceID, sessionID)
}

// AddBriefFeedback appends an immutable feedback row for a brief owned by the
// workspace. The brief itself is never mutated.
func (s *Store) AddBriefFeedback(ctx context.Context, workspaceID, actorID, briefID string, rating int, comment string) error {
	if rating < -1 || rating > 1 {
		return fmt.Errorf("rating must be between -1 and 1")
	}
	if len([]rune(comment)) > 2000 {
		return fmt.Errorf("comment exceeds 2000 characters")
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM briefs WHERE workspace_id=$1 AND id=$2)`, workspaceID, briefID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return pgx.ErrNoRows
	}
	id := StableID("brief-feedback", briefID, actorID, fmt.Sprint(time.Now().UnixNano()))
	_, err := s.pool.Exec(ctx, `
		INSERT INTO brief_feedback (id, brief_id, workspace_id, actor_id, rating, comment)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, id, briefID, workspaceID, actorID, rating, comment)
	return err
}

// ListBriefGaps returns persisted gaps for a brief scoped to the workspace.
func (s *Store) ListBriefGaps(ctx context.Context, workspaceID, briefID string) ([]Gap, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, gap_type, missing_relative_to, explanation
		FROM gaps
		WHERE workspace_id=$1 AND brief_id=$2
		ORDER BY created_at, id
	`, workspaceID, briefID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Gap
	for rows.Next() {
		var g Gap
		if err := rows.Scan(&g.ID, &g.GapType, &g.MissingRelativeTo, &g.Explanation); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Gap is a persisted research gap attached to a brief.
type Gap struct {
	ID                string
	GapType           string
	MissingRelativeTo string
	Explanation       string
}

type CreateSessionInput struct {
	ID          string
	WorkspaceID string
	ProjectID   string
	Title       string
	Mode        string
	EpochID     string
}

func (s *Store) CreateSession(ctx context.Context, in CreateSessionInput) (Session, error) {
	if in.ID == "" {
		in.ID = StableID("session", in.WorkspaceID, in.ProjectID, fmt.Sprint(time.Now().UnixNano()))
	}
	if in.Mode == "" {
		in.Mode = "my_library"
	}
	var out Session
	err := s.pool.QueryRow(ctx, `
		INSERT INTO query_sessions (id, workspace_id, project_id, title, status, index_epoch_id)
		VALUES ($1,$2,$3,$4,'accepted',$5)
		RETURNING id, project_id, title, status, index_epoch_id, created_at, updated_at
	`, in.ID, in.WorkspaceID, in.ProjectID, in.Title, in.EpochID).Scan(&out.ID, &out.ProjectID, &out.Title, &out.Status, &out.IndexEpochID, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return Session{}, err
	}
	out.Phase = "accepted"
	return out, nil
}

func (s *Store) GetSession(ctx context.Context, workspaceID, sessionID string) (Session, error) {
	var out Session
	err := s.pool.QueryRow(ctx, `
		SELECT s.id, s.project_id, s.title, s.status, s.index_epoch_id, s.created_at, s.updated_at,
			COALESCE((SELECT phase FROM query_turns t WHERE t.session_id=s.id ORDER BY turn_no DESC LIMIT 1), s.status)
		FROM query_sessions s
		WHERE s.workspace_id=$1 AND s.id=$2 AND s.deleted_at IS NULL
	`, workspaceID, sessionID).Scan(&out.ID, &out.ProjectID, &out.Title, &out.Status, &out.IndexEpochID, &out.CreatedAt, &out.UpdatedAt, &out.Phase)
	return out, err
}

func (s *Store) ListSessions(ctx context.Context, workspaceID, projectID string) ([]Session, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.id, s.project_id, s.title, s.status, s.index_epoch_id, s.created_at, s.updated_at,
			COALESCE((SELECT phase FROM query_turns t WHERE t.session_id=s.id ORDER BY turn_no DESC LIMIT 1), s.status)
		FROM query_sessions s
		WHERE s.workspace_id=$1 AND s.project_id=$2 AND s.deleted_at IS NULL
		ORDER BY s.created_at DESC
	`, workspaceID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var x Session
		if err := rows.Scan(&x.ID, &x.ProjectID, &x.Title, &x.Status, &x.IndexEpochID, &x.CreatedAt, &x.UpdatedAt, &x.Phase); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) ListTurns(ctx context.Context, workspaceID, sessionID string) ([]Turn, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, session_id, turn_no, question, mode, status, phase, index_epoch_id,
			planner_version, retrieval_config_version, prompt_version, created_at
		FROM query_turns
		WHERE workspace_id=$1 AND session_id=$2
		ORDER BY turn_no
	`, workspaceID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Turn
	for rows.Next() {
		var x Turn
		if err := rows.Scan(&x.ID, &x.SessionID, &x.TurnNo, &x.Question, &x.Mode, &x.Status, &x.Phase, &x.IndexEpochID, &x.PlannerVersion, &x.RetrievalConfigVersion, &x.PromptVersion, &x.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) CollectionIDs(ctx context.Context, workspaceID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM collections
		WHERE workspace_id=$1 AND is_deleted=false
		ORDER BY name, id
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) Coverage(ctx context.Context, workspaceID string) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT index_status, count(*)
		FROM documents
		WHERE workspace_id=$1 AND removed_at IS NULL
		GROUP BY index_status
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}
