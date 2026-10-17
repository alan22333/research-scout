package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"research-scout/internal/domain"
	"research-scout/internal/embedding"
	"research-scout/internal/query"
	"research-scout/internal/storage"
	"research-scout/internal/zotero"
)

func openTestStore(t *testing.T) *storage.Store {
	t.Helper()
	databaseURL := os.Getenv("RS_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RS_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(store.Close)
	if _, err := store.Migrate(ctx, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return store
}

func newTestWorkspace(t *testing.T, store *storage.Store) (string, string) {
	t.Helper()
	workspaceID := storage.StableID("test-workspace", t.Name(), fmt.Sprint(time.Now().UnixNano()))
	actorID := storage.StableID("test-actor", workspaceID)
	ctx := context.Background()
	if err := storage.SeedLocal(ctx, store, actorID, workspaceID); err != nil {
		t.Fatalf("seed test workspace: %v", err)
	}
	t.Cleanup(func() { cleanupWorkspace(t, store, workspaceID, actorID) })
	return workspaceID, actorID
}

func loadStage0Fixture(t *testing.T) zotero.Fixture {
	t.Helper()
	fixture, err := zotero.LoadFixture(filepath.Join("..", "..", "scripts", "fixtures", "zotero-stage0.yaml"))
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	return fixture
}

func importStage0Fixture(t *testing.T, store *storage.Store, workspaceID, actorID string) zotero.ImportResult {
	t.Helper()
	result, err := (zotero.Importer{Store: store, EmbeddingDim: 1024}).Import(context.Background(), workspaceID, actorID, loadStage0Fixture(t))
	if err != nil {
		t.Fatalf("import fixture: %v", err)
	}
	return result
}

func newTestEngine() *query.Engine {
	return &query.Engine{Stub: embedding.Stub{Dim: 1024}}
}

func createAllLibraryProject(t *testing.T, store *storage.Store, workspaceID, name string) storage.Project {
	t.Helper()
	project, err := store.CreateProject(context.Background(), storage.Project{
		ID: storage.StableID("test-project", workspaceID, name), WorkspaceID: workspaceID,
		Name: name, DefaultMode: "my_library", ScopeMode: "all_library",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}

func TestIntegrationStage0Smoke(t *testing.T) {
	store := openTestStore(t)
	workspaceID, actorID := newTestWorkspace(t, store)
	imported := importStage0Fixture(t, store, workspaceID, actorID)
	if imported.Documents != 3 || imported.Chunks != 4 || imported.MetadataOnly != 1 {
		t.Fatalf("unexpected import result: %+v", imported)
	}

	engine := newTestEngine()
	engine.Store = store
	project := createAllLibraryProject(t, store, workspaceID, "stage0 smoke")
	result, err := engine.RunFirst(context.Background(), workspaceID, project.ID, "hybrid retrieval reciprocal rank fusion", domain.RetrievalModeMyLibrary)
	if err != nil {
		t.Fatalf("run stage 0 query: %v", err)
	}
	if result.Brief.IndexEpochID != imported.EpochID {
		t.Fatalf("brief pinned to %q, want active epoch %q", result.Brief.IndexEpochID, imported.EpochID)
	}
	if len(result.Brief.Citations) == 0 {
		t.Fatal("expected at least one citation")
	}
	if len(result.Brief.SummaryClaims) == 0 || len(result.Brief.Findings) == 0 {
		t.Fatal("expected traceable summary and finding claims")
	}
	claims := append([]query.ClaimView{}, result.Brief.SummaryClaims...)
	for _, finding := range result.Brief.Findings {
		claims = append(claims, query.ClaimView{
			ClaimID: finding.ClaimID, ClaimType: finding.ClaimType, Text: finding.Claim,
			SupportLevel: finding.SupportLevel, Confidence: finding.Confidence,
			VerificationStatus: finding.VerificationStatus, ClaimEvidence: finding.ClaimEvidence,
		})
	}
	for _, claim := range claims {
		if len(claim.ClaimEvidence) == 0 {
			t.Fatalf("claim %s has no evidence", claim.ClaimID)
		}
	}

	var metadataEvidence int
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT count(*) FROM evidence e
		JOIN documents d ON d.id=e.document_id
		WHERE e.workspace_id=$1 AND e.brief_id=$2 AND d.index_status='metadata_only'
	`, workspaceID, result.Brief.ID).Scan(&metadataEvidence); err != nil {
		t.Fatalf("count metadata-only evidence: %v", err)
	}
	if metadataEvidence != 0 {
		t.Fatalf("metadata-only documents entered evidence: %d", metadataEvidence)
	}
}

func TestIntegrationFixtureImportIdempotent(t *testing.T) {
	store := openTestStore(t)
	workspaceID, actorID := newTestWorkspace(t, store)
	first := importStage0Fixture(t, store, workspaceID, actorID)
	second := importStage0Fixture(t, store, workspaceID, actorID)
	if second.NewEpoch {
		t.Fatal("second import created a new epoch")
	}
	if first.EpochID != second.EpochID {
		t.Fatalf("epoch changed across identical imports: %s != %s", first.EpochID, second.EpochID)
	}
	assertWorkspaceCount(t, store, workspaceID, "documents", 3)
	assertWorkspaceCount(t, store, workspaceID, "document_versions", 3)
	assertWorkspaceCount(t, store, workspaceID, "chunks", 4)
	assertWorkspaceCount(t, store, workspaceID, "index_epochs", 1)
	assertWorkspaceCount(t, store, workspaceID, "index_epoch_documents", 3)
}

func TestIntegrationQueryUsesActiveEpochNotBuilding(t *testing.T) {
	store := openTestStore(t)
	workspaceID, actorID := newTestWorkspace(t, store)
	imported := importStage0Fixture(t, store, workspaceID, actorID)

	var versionID, documentID string
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT dv.id, dv.document_id
		FROM document_versions dv
		JOIN documents d ON d.id=dv.document_id
		WHERE dv.workspace_id=$1 AND d.zotero_item_key='ITEM-FULLTEXT-001'
		LIMIT 1
	`, workspaceID).Scan(&versionID, &documentID); err != nil {
		t.Fatalf("find fixture document version: %v", err)
	}
	buildingID := storage.StableID("building-epoch", workspaceID, t.Name())
	if _, err := store.Pool().Exec(context.Background(), `
		INSERT INTO index_epochs (id,workspace_id,status,embedding_version,retrieval_config_version,content_hash)
		VALUES ($1,$2,'building',$3,'retrieval-v1','building-only')
	`, buildingID, workspaceID, embedding.Stage0Version); err != nil {
		t.Fatalf("insert building epoch: %v", err)
	}
	if _, err := store.Pool().Exec(context.Background(), `
		INSERT INTO chunks
			(id,index_epoch_id,document_version_id,document_id,workspace_id,ordinal,section,content,token_count,search_tsv,embedding,embedding_version)
		VALUES ($1,$2,$3,$4,$5,99,'building-only','building epoch secret must not leak',
			6, to_tsvector('english','building epoch secret must not leak'), $6::vector, $7)
	`, storage.StableID("building-chunk", buildingID, versionID), buildingID, versionID, documentID, workspaceID,
		embedding.FormatVector(embedding.Stub{Dim: 1024}.Vector("building epoch secret must not leak")), embedding.Stage0Version); err != nil {
		t.Fatalf("insert building chunk: %v", err)
	}

	engine := newTestEngine()
	engine.Store = store
	project := createAllLibraryProject(t, store, workspaceID, "active epoch")
	result, err := engine.RunFirst(context.Background(), workspaceID, project.ID, "building epoch secret", domain.RetrievalModeMyLibrary)
	if err != nil {
		t.Fatalf("run query with building epoch present: %v", err)
	}
	if result.Brief.IndexEpochID != imported.EpochID {
		t.Fatalf("query pinned to %q, want active epoch %q", result.Brief.IndexEpochID, imported.EpochID)
	}
	for _, citation := range result.Brief.Citations {
		if strings.Contains(citation.Title, "building") || (citation.Quote != nil && strings.Contains(*citation.Quote, "must not leak")) {
			t.Fatalf("building epoch content leaked into brief: %+v", citation)
		}
	}
}

func TestIntegrationProjectScopeFiltersCollections(t *testing.T) {
	store := openTestStore(t)
	workspaceID, actorID := newTestWorkspace(t, store)
	importStage0Fixture(t, store, workspaceID, actorID)
	collectionIDs, err := store.CollectionIDs(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("list collections: %v", err)
	}
	if len(collectionIDs) != 2 {
		t.Fatalf("expected 2 collections, got %d", len(collectionIDs))
	}
	var evaluationID string
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT id FROM collections WHERE workspace_id=$1 AND zotero_key='COLL-EVALUATION'
	`, workspaceID).Scan(&evaluationID); err != nil {
		t.Fatalf("find evaluation collection: %v", err)
	}
	project, err := store.CreateProject(context.Background(), storage.Project{
		ID: storage.StableID("test-project", workspaceID, "scope"), WorkspaceID: workspaceID,
		Name: "scope", DefaultMode: "my_library", ScopeMode: "selected_collections", CollectionIDs: []string{evaluationID},
	})
	if err != nil {
		t.Fatalf("create selected-scope project: %v", err)
	}

	engine := newTestEngine()
	engine.Store = store
	result, err := engine.RunFirst(context.Background(), workspaceID, project.ID, "hybrid retrieval reciprocal rank fusion", domain.RetrievalModeMyLibrary)
	if err != nil {
		t.Fatalf("run scoped query: %v", err)
	}
	for _, citation := range result.Brief.Citations {
		if citation.DocumentID == nil {
			continue
		}
		var count int
		if err := store.Pool().QueryRow(context.Background(), `
			SELECT count(*) FROM document_collections
			WHERE workspace_id=$1 AND document_id=$2 AND collection_id=$3
		`, workspaceID, *citation.DocumentID, evaluationID).Scan(&count); err != nil {
			t.Fatalf("check citation scope: %v", err)
		}
		if count != 1 {
			t.Fatalf("citation %s escaped selected scope", *citation.DocumentID)
		}
	}
}

func assertWorkspaceCount(t *testing.T, store *storage.Store, workspaceID, table string, want int) {
	t.Helper()
	var got int
	query := "SELECT count(*) FROM " + table + " WHERE workspace_id=$1"
	if err := store.Pool().QueryRow(context.Background(), query, workspaceID).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func cleanupWorkspace(t *testing.T, store *storage.Store, workspaceID, actorID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	statements := []string{
		`DELETE FROM brief_feedback WHERE workspace_id=$1`,
		`DELETE FROM claim_evidence WHERE workspace_id=$1`,
		`DELETE FROM claims WHERE workspace_id=$1`,
		`DELETE FROM evidence WHERE workspace_id=$1`,
		`DELETE FROM gaps WHERE workspace_id=$1`,
		`DELETE FROM briefs WHERE workspace_id=$1`,
		`DELETE FROM query_events WHERE workspace_id=$1`,
		`DELETE FROM query_turns WHERE workspace_id=$1`,
		`DELETE FROM query_sessions WHERE workspace_id=$1`,
		`DELETE FROM projects WHERE workspace_id=$1`,
		`DELETE FROM lifecycle_tombstones WHERE workspace_id=$1`,
		`DELETE FROM audit_events WHERE workspace_id=$1`,
		`DELETE FROM external_citation_edges WHERE workspace_id=$1`,
		`DELETE FROM external_works WHERE workspace_id=$1`,
		`DELETE FROM chunks WHERE workspace_id=$1`,
		`DELETE FROM index_epoch_documents WHERE workspace_id=$1`,
		`DELETE FROM index_epochs WHERE workspace_id=$1`,
		`DELETE FROM document_collections WHERE workspace_id=$1`,
		`DELETE FROM document_versions WHERE workspace_id=$1`,
		`DELETE FROM documents WHERE workspace_id=$1`,
		`DELETE FROM collections WHERE workspace_id=$1`,
		`DELETE FROM zotero_sync_runs WHERE workspace_id=$1`,
		`DELETE FROM zotero_connections WHERE workspace_id=$1`,
		`DELETE FROM workspace_members WHERE workspace_id=$1`,
		`DELETE FROM workspaces WHERE id=$1`,
		`DELETE FROM actors WHERE id=$1`,
	}
	for _, statement := range statements {
		arg := any(workspaceID)
		if strings.HasPrefix(statement, "DELETE FROM actors") {
			arg = actorID
		}
		if _, err := store.Pool().Exec(ctx, statement, arg); err != nil {
			t.Logf("cleanup warning after %s: %v", statement, err)
			return
		}
	}
}
