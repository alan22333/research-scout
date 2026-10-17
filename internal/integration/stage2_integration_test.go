package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"research-scout/internal/config"
	"research-scout/internal/domain"
	"research-scout/internal/httpserver"
	"research-scout/internal/identity"
	"research-scout/internal/planner"
	"research-scout/internal/query"
	"research-scout/internal/storage"
)

func newStage2Router(store *storage.Store, engine *query.Engine, workspaceID, actorID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	return httpserver.NewRouter(config.Config{DevAuthEnabled: true}, store, engine,
		identity.LocalAdapter{ActorID: actorID, WorkspaceID: workspaceID})
}

func stage2Request(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func stage2ErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response %d %q: %v", rec.Code, rec.Body.String(), err)
	}
	return payload.Error.Code
}

func hasSourceStatus(statuses []query.SourceStatus, source, status string) bool {
	for _, item := range statuses {
		if item.Source == source && item.Status == status {
			return true
		}
	}
	return false
}

func TestIntegrationStage2GroundedBrief(t *testing.T) {
	store := openTestStore(t)
	workspaceID, actorID := newTestWorkspace(t, store)
	importStage0Fixture(t, store, workspaceID, actorID)

	var evaluationCollectionID string
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT id FROM collections WHERE workspace_id=$1 AND zotero_key='COLL-EVALUATION'
	`, workspaceID).Scan(&evaluationCollectionID); err != nil {
		t.Fatalf("find evaluation collection: %v", err)
	}
	project, err := store.CreateProject(context.Background(), storage.Project{
		ID: storage.StableID("test-project", workspaceID, "stage2-grounded"), WorkspaceID: workspaceID,
		Name: "stage2 grounded", DefaultMode: "my_library", ScopeMode: "selected_collections",
		CollectionIDs: []string{evaluationCollectionID},
	})
	if err != nil {
		t.Fatalf("create selected-scope project: %v", err)
	}

	engine := newTestEngine()
	engine.Store = store
	result, err := engine.RunFirst(context.Background(), workspaceID, project.ID,
		"grounded citations retrieval coverage RAG benchmark", domain.RetrievalModeMyLibrary)
	if err != nil {
		t.Fatalf("run grounded Stage 2 query: %v", err)
	}
	if len(result.Brief.Citations) == 0 || len(result.Brief.SummaryClaims) == 0 || len(result.Brief.Findings) == 0 {
		t.Fatalf("expected abstract evidence and a partial grounded brief: %+v", result.Brief)
	}
	for _, citation := range result.Brief.Citations {
		if citation.EvidenceType != string(domain.EvidencePersonalFulltext) && citation.EvidenceType != string(domain.EvidenceStructuredAbstract) {
			t.Fatalf("ineligible evidence type entered brief: %+v", citation)
		}
	}
	if !hasSourceStatus(result.Brief.SourceStatus, "rerank", "degraded") {
		t.Fatalf("rerank fallback missing from brief source_status: %+v", result.Brief.SourceStatus)
	}
	for _, finding := range result.Brief.Findings {
		if finding.VerificationStatus == string(domain.VerificationVerified) {
			t.Fatalf("abstract-only finding must not be verified: %+v", finding)
		}
		if finding.SupportLevel != "partial" {
			t.Fatalf("abstract-only finding support=%q want partial", finding.SupportLevel)
		}
	}
	if got := result.Brief.SummaryClaims[0].VerificationStatus; got == string(domain.VerificationVerified) {
		t.Fatalf("abstract-only summary must not be verified: %+v", result.Brief.SummaryClaims[0])
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
		t.Fatalf("metadata-only document entered evidence: %d", metadataEvidence)
	}

	var planRaw []byte
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT plan_json FROM query_turns WHERE workspace_id=$1 AND id=$2
	`, workspaceID, result.Turn.ID).Scan(&planRaw); err != nil {
		t.Fatalf("load persisted plan: %v", err)
	}
	var plan planner.QueryPlan
	if err := json.Unmarshal(planRaw, &plan); err != nil {
		t.Fatalf("decode persisted plan: %v", err)
	}
	if plan.Version != planner.Version || len(plan.Subqueries) == 0 || len(plan.Subqueries) > 5 {
		t.Fatalf("unexpected persisted plan: %+v", plan)
	}
	var planEvents int
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT count(*) FROM query_events
		WHERE workspace_id=$1 AND session_id=$2 AND type='plan_updated'
	`, workspaceID, result.Session.ID).Scan(&planEvents); err != nil {
		t.Fatalf("count plan_updated events: %v", err)
	}
	if planEvents == 0 {
		t.Fatal("expected persisted plan_updated event")
	}

	allLibrary, err := store.CreateProject(context.Background(), storage.Project{
		ID: storage.StableID("test-project", workspaceID, "stage2-empty"), WorkspaceID: workspaceID,
		Name: "stage2 empty", DefaultMode: "my_library", ScopeMode: "all_library",
	})
	if err != nil {
		t.Fatalf("create empty-query project: %v", err)
	}
	empty, err := engine.RunFirst(context.Background(), workspaceID, allLibrary.ID,
		"zzzzunmatchedevidencezzzz", domain.RetrievalModeMyLibrary)
	if err != nil {
		t.Fatalf("run no-evidence query: %v", err)
	}
	if !strings.Contains(strings.ToLower(empty.Brief.Summary), "insufficient") {
		t.Fatalf("no-evidence summary=%q want insufficient", empty.Brief.Summary)
	}
	if len(empty.Brief.Findings) != 0 {
		t.Fatalf("no-evidence brief fabricated findings: %+v", empty.Brief.Findings)
	}
	if len(empty.Brief.OpenQuestions) == 0 {
		t.Fatal("no-evidence brief must state an open question")
	}
}

func TestIntegrationStage2ScopeValidation(t *testing.T) {
	store := openTestStore(t)
	workspaceID, actorID := newTestWorkspace(t, store)
	importStage0Fixture(t, store, workspaceID, actorID)
	project := createAllLibraryProject(t, store, workspaceID, "stage2 scope validation")
	router := newStage2Router(store, nil, workspaceID, actorID)

	var evaluationCollectionID string
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT id FROM collections WHERE workspace_id=$1 AND zotero_key='COLL-EVALUATION'
	`, workspaceID).Scan(&evaluationCollectionID); err != nil {
		t.Fatalf("find evaluation collection: %v", err)
	}

	empty := stage2Request(t, router, http.MethodPut, "/api/v1/projects/"+project.ID+"/scope", map[string]any{
		"scope_mode": "selected_collections", "collection_ids": []string{},
	})
	if empty.Code != http.StatusUnprocessableEntity || stage2ErrorCode(t, empty) != "scope_empty" {
		t.Fatalf("empty scope response=%d %s", empty.Code, empty.Body.String())
	}

	foreign := stage2Request(t, router, http.MethodPut, "/api/v1/projects/"+project.ID+"/scope", map[string]any{
		"scope_mode": "selected_collections", "collection_ids": []string{"collection-from-another-workspace"},
	})
	if foreign.Code != http.StatusForbidden || stage2ErrorCode(t, foreign) != "forbidden" {
		t.Fatalf("foreign scope response=%d %s", foreign.Code, foreign.Body.String())
	}

	if _, err := store.Pool().Exec(context.Background(), `
		UPDATE collections SET is_deleted=true WHERE workspace_id=$1 AND id=$2
	`, workspaceID, evaluationCollectionID); err != nil {
		t.Fatalf("soft-delete collection: %v", err)
	}
	deleted := stage2Request(t, router, http.MethodPut, "/api/v1/projects/"+project.ID+"/scope", map[string]any{
		"scope_mode": "selected_collections", "collection_ids": []string{evaluationCollectionID},
	})
	if deleted.Code != http.StatusForbidden || stage2ErrorCode(t, deleted) != "forbidden" {
		t.Fatalf("deleted scope response=%d %s", deleted.Code, deleted.Body.String())
	}
	if _, err := store.Pool().Exec(context.Background(), `
		UPDATE collections SET is_deleted=false WHERE workspace_id=$1 AND id=$2
	`, workspaceID, evaluationCollectionID); err != nil {
		t.Fatalf("restore collection: %v", err)
	}

	allLibrary := stage2Request(t, router, http.MethodPut, "/api/v1/projects/"+project.ID+"/scope", map[string]any{
		"scope_mode": "all_library", "collection_ids": []string{evaluationCollectionID},
	})
	if allLibrary.Code != http.StatusOK {
		t.Fatalf("all-library scope response=%d %s", allLibrary.Code, allLibrary.Body.String())
	}
	var payload struct {
		ScopeMode     string   `json:"scope_mode"`
		CollectionIDs []string `json:"collection_ids"`
	}
	if err := json.Unmarshal(allLibrary.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode scope response: %v", err)
	}
	if payload.ScopeMode != "all_library" || len(payload.CollectionIDs) != 0 {
		t.Fatalf("all_library did not clear collection ids: %+v", payload)
	}

	var storedMode string
	var scopeRaw []byte
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT scope_mode, collection_ids_json FROM projects WHERE workspace_id=$1 AND id=$2
	`, workspaceID, project.ID).Scan(&storedMode, &scopeRaw); err != nil {
		t.Fatalf("load persisted scope: %v", err)
	}
	var persisted []string
	if err := json.Unmarshal(scopeRaw, &persisted); err != nil {
		t.Fatalf("decode persisted scope: %v", err)
	}
	if storedMode != "all_library" || len(persisted) != 0 {
		t.Fatalf("persisted scope=%s/%v want all_library/[]", storedMode, persisted)
	}
}

func TestIntegrationStage2LifecycleRoutes(t *testing.T) {
	store := openTestStore(t)
	workspaceID, actorID := newTestWorkspace(t, store)
	importStage0Fixture(t, store, workspaceID, actorID)
	project := createAllLibraryProject(t, store, workspaceID, "stage2 lifecycle")
	engine := newTestEngine()
	engine.Store = store
	result, err := engine.RunFirst(context.Background(), workspaceID, project.ID,
		"hybrid retrieval reciprocal rank fusion", domain.RetrievalModeMyLibrary)
	if err != nil {
		t.Fatalf("run lifecycle fixture query: %v", err)
	}
	router := newStage2Router(store, engine, workspaceID, actorID)

	if _, err := store.Pool().Exec(context.Background(), `
		UPDATE query_sessions SET status='running' WHERE workspace_id=$1 AND id=$2
	`, workspaceID, result.Session.ID); err != nil {
		t.Fatalf("set session running: %v", err)
	}
	cancel := stage2Request(t, router, http.MethodPost, "/api/v1/sessions/"+result.Session.ID+"/cancel", nil)
	if cancel.Code != http.StatusAccepted {
		t.Fatalf("cancel response=%d %s", cancel.Code, cancel.Body.String())
	}
	var canceled struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(cancel.Body.Bytes(), &canceled); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if canceled.Status != "canceled" {
		t.Fatalf("cancel status=%q want canceled", canceled.Status)
	}

	deleteSession := stage2Request(t, router, http.MethodDelete, "/api/v1/sessions/"+result.Session.ID, nil)
	if deleteSession.Code != http.StatusAccepted {
		t.Fatalf("delete session response=%d %s", deleteSession.Code, deleteSession.Body.String())
	}
	getSession := stage2Request(t, router, http.MethodGet, "/api/v1/sessions/"+result.Session.ID, nil)
	if getSession.Code != http.StatusNotFound {
		t.Fatalf("deleted session visible: %d %s", getSession.Code, getSession.Body.String())
	}
	var deletedSession bool
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT deleted_at IS NOT NULL FROM query_sessions WHERE workspace_id=$1 AND id=$2
	`, workspaceID, result.Session.ID).Scan(&deletedSession); err != nil {
		t.Fatalf("load deleted session: %v", err)
	}
	if !deletedSession {
		t.Fatal("session was not soft-deleted")
	}

	deleteProject := stage2Request(t, router, http.MethodDelete, "/api/v1/projects/"+project.ID, nil)
	if deleteProject.Code != http.StatusAccepted {
		t.Fatalf("delete project response=%d %s", deleteProject.Code, deleteProject.Body.String())
	}
	getProject := stage2Request(t, router, http.MethodGet, "/api/v1/projects/"+project.ID, nil)
	if getProject.Code != http.StatusNotFound {
		t.Fatalf("deleted project visible: %d %s", getProject.Code, getProject.Body.String())
	}
}

func TestIntegrationStage2BriefFeedbackAndExport(t *testing.T) {
	store := openTestStore(t)
	workspaceID, actorID := newTestWorkspace(t, store)
	importStage0Fixture(t, store, workspaceID, actorID)
	project := createAllLibraryProject(t, store, workspaceID, "stage2 export")
	engine := newTestEngine()
	engine.Store = store
	result, err := engine.RunFirst(context.Background(), workspaceID, project.ID,
		"hybrid retrieval reciprocal rank fusion", domain.RetrievalModeMyLibrary)
	if err != nil {
		t.Fatalf("run export fixture query: %v", err)
	}
	if len(result.Brief.Citations) == 0 {
		t.Fatal("export fixture requires citations")
	}
	router := newStage2Router(store, engine, workspaceID, actorID)

	feedback := stage2Request(t, router, http.MethodPost, "/api/v1/briefs/"+result.Brief.ID+"/feedback", map[string]any{
		"rating": 1, "comment": "Useful evidence trail",
	})
	if feedback.Code != http.StatusNoContent {
		t.Fatalf("feedback response=%d %s", feedback.Code, feedback.Body.String())
	}
	var feedbackCount int
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT count(*) FROM brief_feedback WHERE workspace_id=$1 AND brief_id=$2
	`, workspaceID, result.Brief.ID).Scan(&feedbackCount); err != nil {
		t.Fatalf("count brief feedback: %v", err)
	}
	if feedbackCount != 1 {
		t.Fatalf("feedback rows=%d want 1", feedbackCount)
	}

	invalidFeedback := stage2Request(t, router, http.MethodPost, "/api/v1/briefs/"+result.Brief.ID+"/feedback", map[string]any{
		"rating": 2,
	})
	if invalidFeedback.Code != http.StatusBadRequest {
		t.Fatalf("invalid feedback response=%d %s", invalidFeedback.Code, invalidFeedback.Body.String())
	}

	var summaryBefore string
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT summary FROM briefs WHERE workspace_id=$1 AND id=$2
	`, workspaceID, result.Brief.ID).Scan(&summaryBefore); err != nil {
		t.Fatalf("load brief before feedback: %v", err)
	}

	for _, format := range []string{"json", "bibtex", "csv"} {
		rec := stage2Request(t, router, http.MethodGet, "/api/v1/briefs/"+result.Brief.ID+"/export?format="+format, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s export response=%d %s", format, rec.Code, rec.Body.String())
		}
		if !strings.Contains(strings.ToLower(rec.Body.String()), "hybrid retrieval") {
			t.Fatalf("%s export omitted authorized citation: %s", format, rec.Body.String())
		}
	}

	var summaryAfter string
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT summary FROM briefs WHERE workspace_id=$1 AND id=$2
	`, workspaceID, result.Brief.ID).Scan(&summaryAfter); err != nil {
		t.Fatalf("load brief after feedback: %v", err)
	}
	if summaryAfter != summaryBefore {
		t.Fatalf("feedback mutated immutable brief: %q != %q", summaryAfter, summaryBefore)
	}

	otherWorkspaceID, otherActorID := newTestWorkspace(t, store)
	otherRouter := newStage2Router(store, engine, otherWorkspaceID, otherActorID)
	crossWorkspace := stage2Request(t, otherRouter, http.MethodGet, "/api/v1/briefs/"+result.Brief.ID+"/export?format=json", nil)
	if crossWorkspace.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace export response=%d %s", crossWorkspace.Code, crossWorkspace.Body.String())
	}
}
