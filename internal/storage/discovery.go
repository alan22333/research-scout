package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned when a workspace-scoped row does not exist.
var ErrNotFound = errors.New("not found")

// ExternalWorkRecord is the persistence shape of one normalized external work.
type ExternalWorkRecord struct {
	ID            string
	WorkspaceID   string
	IdentityKey   string
	DOI           string
	S2ID          string
	OpenAlexID    string
	PMID          string
	Title         string
	Abstract      string
	Year          int
	Venue         string
	CitationCount int
	Type          string
	Status        string
	Sources       []string
	RetrievedAt   time.Time
	CreatedAt     time.Time
}

// ExternalWorkID derives the stable, workspace-scoped row id for an identity key.
func ExternalWorkID(workspaceID, identityKey string) string {
	return StableID("external", workspaceID, identityKey)
}

// UpsertExternalWorks inserts or refreshes normalized external works. Rows are
// unique per (workspace_id, identity_key); a repeat sync never duplicates.
func (s *Store) UpsertExternalWorks(ctx context.Context, workspaceID string, works []ExternalWorkRecord) error {
	if len(works) == 0 {
		return nil
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, w := range works {
		if w.IdentityKey == "" {
			return fmt.Errorf("external work identity key is required")
		}
		id := w.ID
		if id == "" {
			id = ExternalWorkID(workspaceID, w.IdentityKey)
		}
		sources, err := JSON(defaultStrings(w.Sources))
		if err != nil {
			return err
		}
		status := w.Status
		if status == "" {
			status = "available"
		}
		retrievedAt := w.RetrievedAt
		if retrievedAt.IsZero() {
			retrievedAt = time.Now().UTC()
		}
		var year any
		if w.Year > 0 {
			year = w.Year
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO external_works
				(id, workspace_id, identity_key, doi_normalized, s2_id, openalex_id, pmid, title, abstract,
				 year, venue, citation_count, type, status, sources_json, retrieved_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb,$16,now())
			ON CONFLICT (workspace_id, identity_key) DO UPDATE SET
				doi_normalized = EXCLUDED.doi_normalized,
				s2_id = EXCLUDED.s2_id,
				openalex_id = EXCLUDED.openalex_id,
				pmid = EXCLUDED.pmid,
				title = EXCLUDED.title,
				abstract = CASE WHEN EXCLUDED.abstract <> '' THEN EXCLUDED.abstract ELSE external_works.abstract END,
				year = COALESCE(EXCLUDED.year, external_works.year),
				venue = CASE WHEN EXCLUDED.venue <> '' THEN EXCLUDED.venue ELSE external_works.venue END,
				citation_count = GREATEST(EXCLUDED.citation_count, external_works.citation_count),
				type = CASE WHEN EXCLUDED.type <> '' THEN EXCLUDED.type ELSE external_works.type END,
				status = EXCLUDED.status,
				sources_json = EXCLUDED.sources_json,
				retrieved_at = EXCLUDED.retrieved_at,
				updated_at = now()
		`, id, workspaceID, w.IdentityKey, w.DOI, w.S2ID, w.OpenAlexID, w.PMID, w.Title, w.Abstract,
			year, w.Venue, w.CitationCount, w.Type, status, sources, retrievedAt); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// GetExternalWork reads one external work, always scoped to the workspace.
func (s *Store) GetExternalWork(ctx context.Context, workspaceID, id string) (ExternalWorkRecord, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, workspace_id, identity_key, doi_normalized, s2_id, openalex_id, pmid, title, abstract,
			COALESCE(year,0), venue, citation_count, type, status, sources_json, retrieved_at, first_seen_at
		FROM external_works
		WHERE workspace_id=$1 AND id=$2
	`, workspaceID, id)
	return scanExternalWork(row)
}

// ListExternalWorksByIDs reads several works within one workspace.
func (s *Store) ListExternalWorksByIDs(ctx context.Context, workspaceID string, ids []string) ([]ExternalWorkRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, workspace_id, identity_key, doi_normalized, s2_id, openalex_id, pmid, title, abstract,
			COALESCE(year,0), venue, citation_count, type, status, sources_json, retrieved_at, first_seen_at
		FROM external_works
		WHERE workspace_id=$1 AND id = ANY($2)
	`, workspaceID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExternalWorkRecord
	for rows.Next() {
		rec, err := scanExternalWork(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

type externalWorkScanner interface {
	Scan(dest ...any) error
}

func scanExternalWork(row externalWorkScanner) (ExternalWorkRecord, error) {
	var rec ExternalWorkRecord
	var sources []byte
	err := row.Scan(&rec.ID, &rec.WorkspaceID, &rec.IdentityKey, &rec.DOI, &rec.S2ID, &rec.OpenAlexID, &rec.PMID,
		&rec.Title, &rec.Abstract, &rec.Year, &rec.Venue, &rec.CitationCount, &rec.Type, &rec.Status,
		&sources, &rec.RetrievedAt, &rec.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExternalWorkRecord{}, ErrNotFound
	}
	if err != nil {
		return ExternalWorkRecord{}, err
	}
	rec.Sources = decodeStringArray(sources)
	return rec, nil
}

// CitationEdge is one read-only citation/reference relation stored for a work.
type CitationEdge struct {
	Direction          string
	RelatedIdentityKey string
	RelatedDOI         string
	RelatedTitle       string
	RelatedYear        int
	Provider           string
}

// UpsertCitationEdges stores read-only citation relations for one external work.
func (s *Store) UpsertCitationEdges(ctx context.Context, workspaceID, externalWorkID string, edges []CitationEdge) error {
	if len(edges) == 0 {
		return nil
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, e := range edges {
		if e.RelatedIdentityKey == "" {
			continue
		}
		id := StableID("citedge", workspaceID, externalWorkID, e.Direction, e.RelatedIdentityKey)
		if _, err := tx.Exec(ctx, `
			INSERT INTO external_citation_edges
				(id, workspace_id, external_work_id, direction, related_identity_key, related_doi, related_title, related_year, provider)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (workspace_id, external_work_id, direction, related_identity_key) DO UPDATE SET
				related_doi = EXCLUDED.related_doi,
				related_title = EXCLUDED.related_title,
				related_year = EXCLUDED.related_year,
				provider = EXCLUDED.provider,
				retrieved_at = now()
		`, id, workspaceID, externalWorkID, e.Direction, e.RelatedIdentityKey, e.RelatedDOI, e.RelatedTitle, e.RelatedYear, e.Provider); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ListCitedWorks returns stored citation relations joined back to external works
// in the same workspace. Stage 3 never expands a second discovery hop.
func (s *Store) ListCitedWorks(ctx context.Context, workspaceID, externalWorkID string) ([]ExternalWorkRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT w.id, w.workspace_id, w.identity_key, w.doi_normalized, w.s2_id, w.openalex_id, w.pmid,
			w.title, w.abstract, COALESCE(w.year,0), w.venue, w.citation_count, w.type, w.status,
			w.sources_json, w.retrieved_at, w.first_seen_at
		FROM external_citation_edges e
		JOIN external_works w
			ON w.workspace_id = e.workspace_id AND w.identity_key = e.related_identity_key
		WHERE e.workspace_id=$1 AND e.external_work_id=$2
		ORDER BY w.citation_count DESC, w.identity_key
	`, workspaceID, externalWorkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExternalWorkRecord
	for rows.Next() {
		rec, err := scanExternalWork(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// LibraryProjection is the minimal personal-library view used for external
// de-duplication and gap detection.
type LibraryProjection struct {
	DocumentID string
	Title      string
	Year       int
	DOI        string
	InScope    bool
	Missing    bool
}

// LibraryProjectionForScope returns every document of the My Library plus
// whether it is inside the given collection scope. Tombstoned documents are
// reported with Missing=true so they never count as library coverage.
func (s *Store) LibraryProjectionForScope(ctx context.Context, workspaceID, scopeMode string, collectionIDs []string) ([]LibraryProjection, error) {
	scopeJSON, err := JSON(defaultStrings(collectionIDs))
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.title, COALESCE(d.year,0), d.doi_normalized,
			(d.removed_at IS NULL) AS present,
			(
				d.removed_at IS NULL
				AND (
					$2 = 'all_library'
					OR EXISTS (
						SELECT 1 FROM document_collections dc
						WHERE dc.workspace_id = d.workspace_id
							AND dc.document_id = d.id
							AND dc.collection_id IN (SELECT jsonb_array_elements_text($3::jsonb))
					)
				)
			) AS in_scope
		FROM documents d
		WHERE d.workspace_id=$1
		ORDER BY d.id
	`, workspaceID, scopeMode, scopeJSON)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LibraryProjection
	for rows.Next() {
		var row LibraryProjection
		var present bool
		if err := rows.Scan(&row.DocumentID, &row.Title, &row.Year, &row.DOI, &present, &row.InScope); err != nil {
			return nil, err
		}
		row.Missing = !present
		if row.Missing {
			row.InScope = false
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// GapRecord is the persistence shape of one detected research gap.
type GapRecord struct {
	ID                string
	GapType           string
	MissingRelativeTo string
	Explanation       string
	ExternalWorkID    string
	RationaleJSON     map[string]any
	Score             float64
}

// PersistGaps stores the gaps of one brief. Repeat writes are idempotent.
func (s *Store) PersistGaps(ctx context.Context, workspaceID, briefID string, gaps []GapRecord) error {
	if len(gaps) == 0 {
		return nil
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for i, g := range gaps {
		id := g.ID
		if id == "" {
			id = StableID("gap", briefID, g.GapType, g.ExternalWorkID, fmt.Sprint(i))
		}
		rationale, err := JSON(g.RationaleJSON)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO gaps
				(id, brief_id, workspace_id, gap_type, missing_relative_to, explanation, external_work_id, rationale_json, score)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9)
			ON CONFLICT (id) DO UPDATE SET
				explanation = EXCLUDED.explanation,
				external_work_id = EXCLUDED.external_work_id,
				rationale_json = EXCLUDED.rationale_json,
				score = EXCLUDED.score
		`, id, briefID, workspaceID, g.GapType, g.MissingRelativeTo, g.Explanation, g.ExternalWorkID, rationale, g.Score); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func defaultStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func decodeStringArray(raw []byte) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return []string{}
	}
	if out == nil {
		return []string{}
	}
	return out
}
