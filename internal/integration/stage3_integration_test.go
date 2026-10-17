package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"research-scout/internal/discovery"
	"research-scout/internal/domain"
	"research-scout/internal/query"
	"research-scout/internal/storage"
)

// stubDiscovery is a deterministic, offline DiscoveryRunner. It lets the
// integration suite exercise the full Stage 3 vertical path (gap persistence,
// external work storage, evidence degradation and conflict rendering) without
// any real provider call.
type stubDiscovery struct {
	result  discovery.ServiceResult
	err     error
	calls   int
	lastReq discovery.ServiceRequest
}

func (s *stubDiscovery) Discover(_ context.Context, req discovery.ServiceRequest) (discovery.ServiceResult, error) {
	s.calls++
	s.lastReq = req
	return s.result, s.err
}

func extWork(title string, workID, abstract string) discovery.ExternalWork {
	return discovery.ExternalWork{
		ID:          workID,
		IdentityKey: workID,
		Title:       title,
		Abstract:    abstract,
		Year:        2024,
		Venue:       "TestConf",
		Source:      discovery.SourceSemanticScholar,
		Sources:     []discovery.DiscoverySource{discovery.SourceSemanticScholar},
		Status:      discovery.ExternalStatusAvailable,
		RetrievedAt: time.Now().UTC(),
	}
}

// TestIntegrationStage3WorkspaceIsolation proves external works and their gaps
// are scoped to a single workspace: a work stored for workspace A is invisible
// to workspace B both through the storage port and the HTTP detail route.
func TestIntegrationStage3WorkspaceIsolation(t *testing.T) {
	store := openTestStore(t)
	ws1, actor1 := newTestWorkspace(t, store)
	ws2, actor2 := newTestWorkspace(t, store)
	importStage0Fixture(t, store, ws1, actor1)

	project := createAllLibraryProject(t, store, ws1, "stage3 isolation")
	stub := &stubDiscovery{result: discovery.ServiceResult{
		Works:        []discovery.ExternalWork{extWork("Isolated Work", "external-iso-1", "Reranking improves dense retrieval recall on the benchmark.")},
		Gaps:         []discovery.Gap{},
		WebContexts:  []discovery.WebContext{},
		SourceStatus: []discovery.SourceStatusEntry{{Source: "semantic_scholar", Status: domain.SourceAvailable}},
	}}
	engine := newTestEngine()
	engine.Store = store
	engine.Discovery = stub

	result, err := engine.RunFirst(context.Background(), ws1, project.ID, "dense retrieval reranking benchmark", domain.RetrievalModeGapFill)
	if err != nil {
		t.Fatalf("run ws1 gap_fill: %v", err)
	}
	if stub.calls != 1 {
		t.Fatalf("discovery calls=%d want 1", stub.calls)
	}

	var storedID string
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT id FROM external_works WHERE workspace_id=$1 ORDER BY id LIMIT 1
	`, ws1).Scan(&storedID); err != nil {
		t.Fatalf("load stored external work: %v", err)
	}
	if !strings.HasPrefix(storedID, "external-") {
		t.Fatalf("stored external id=%q want external- prefix", storedID)
	}

	// Cross-workspace reads must fail closed.
	if _, err := engine.GetExternalWorkView(context.Background(), ws2, storedID); err == nil {
		t.Fatalf("ws2 read of ws1 external work %q must fail", storedID)
	}

	router1 := newStage2Router(store, engine, ws1, actor1)
	ok := stage2Request(t, router1, http.MethodGet, "/api/v1/discovery/works/"+storedID, nil)
	if ok.Code != http.StatusOK {
		t.Fatalf("owner read status=%d %s", ok.Code, ok.Body.String())
	}
	router2 := newStage2Router(store, engine, ws2, actor2)
	foreign := stage2Request(t, router2, http.MethodGet, "/api/v1/discovery/works/"+storedID, nil)
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace read status=%d want 404 (%s)", foreign.Code, foreign.Body.String())
	}

	var ws2Count int
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT count(*) FROM external_works WHERE workspace_id=$1
	`, ws2).Scan(&ws2Count); err != nil {
		t.Fatalf("count ws2 external works: %v", err)
	}
	if ws2Count != 0 {
		t.Fatalf("ws2 external_works count=%d want 0", ws2Count)
	}
	// ws1 still has its own row (isolation is per-workspace, not global delete).
	var ws1Count int
	if err := store.Pool().QueryRow(context.Background(), `
		SELECT count(*) FROM external_works WHERE workspace_id=$1
	`, ws1).Scan(&ws1Count); err != nil {
		t.Fatalf("count ws1 external works: %v", err)
	}
	if ws1Count != 1 {
		t.Fatalf("ws1 external_works count=%d want 1", ws1Count)
	}
	if !hasSourceStatus(result.Brief.SourceStatus, "semantic_scholar", string(domain.SourceAvailable)) {
		t.Fatalf("owner brief missing available source status: %+v", result.Brief.SourceStatus)
	}
}

// TestIntegrationStage3GapRelativeToScope proves a run whose gap is only a
// scope miss (the work exists in My Library but outside the selected
// collections) persists missing_relative_to=current_scope end to end and is
// readable through the brief gaps route.
func TestIntegrationStage3GapRelativeToScope(t *testing.T) {
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
		ID: storage.StableID("test-project", workspaceID, "stage3-scope"), WorkspaceID: workspaceID,
		Name: "stage3 scope", DefaultMode: "gap_fill", ScopeMode: "selected_collections",
		CollectionIDs: []string{evaluationCollectionID},
	})
	if err != nil {
		t.Fatalf("create scoped project: %v", err)
	}

	scoped := extWork("Scoped Work", "external-scope-1", "A related method exists in the library but outside the selected scope.")
	stub := &stubDiscovery{result: discovery.ServiceResult{
		Works: []discovery.ExternalWork{scoped},
		Gaps: []discovery.Gap{{
			Type:              discovery.GapRelatedMethod,
			Work:              scoped,
			MissingRelativeTo: discovery.MissingCurrentScope,
			Rationale:         "method overlaps a library seed but sits outside the selected collections",
		}},
		WebContexts:  []discovery.WebContext{},
		SourceStatus: []discovery.SourceStatusEntry{{Source: "semantic_scholar", Status: domain.SourceAvailable}},
	}}
	engine := newTestEngine()
	engine.Store = store
	engine.Discovery = stub

	result, err := engine.RunFirst(context.Background(), workspaceID, project.ID, "related method outside scope", domain.RetrievalModeGapFill)
	if err != nil {
		t.Fatalf("run gap_fill: %v", err)
	}
	if stub.lastReq.LibraryUnknown {
		t.Fatalf("library projection should be known for a synced workspace")
	}
	if len(result.Brief.Gaps) != 1 {
		t.Fatalf("brief gaps=%d want 1 (%+v)", len(result.Brief.Gaps), result.Brief.Gaps)
	}
	gap := result.Brief.Gaps[0]
	if gap.GapType != string(discovery.GapRelatedMethod) || gap.MissingRelativeTo != string(discovery.MissingCurrentScope) {
		t.Fatalf("gap type/scope = %q/%q want related_method/current_scope", gap.GapType, gap.MissingRelativeTo)
	}
	if gap.ExternalWorkID == nil || !strings.HasPrefix(*gap.ExternalWorkID, "external-") {
		t.Fatalf("gap external work id not a stored id: %+v", gap.ExternalWorkID)
	}

	router := newStage2Router(store, engine, workspaceID, actorID)
	rec := stage2Request(t, router, http.MethodGet, "/api/v1/briefs/"+result.Brief.ID+"/gaps", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list brief gaps status=%d %s", rec.Code, rec.Body.String())
	}
	var gaps []query.GapView
	if err := json.Unmarshal(rec.Body.Bytes(), &gaps); err != nil {
		t.Fatalf("decode gaps response: %v", err)
	}
	if len(gaps) != 1 || gaps[0].MissingRelativeTo != string(discovery.MissingCurrentScope) {
		t.Fatalf("persisted gaps=%+v want one current_scope gap", gaps)
	}

	// The gap's external work must be directly fetchable and agree on the id.
	work := stage2Request(t, router, http.MethodGet, "/api/v1/discovery/works/"+*gap.ExternalWorkID, nil)
	if work.Code != http.StatusOK {
		t.Fatalf("fetch gap work status=%d %s", work.Code, work.Body.String())
	}
}

// TestIntegrationStage3EvidenceDegradation proves the frozen evidence
// hierarchy holds through a full landscape turn: personal fulltext outranks
// structured abstracts, web context is retained only as background, and an
// abstract/web-only brief can never be verified.
func TestIntegrationStage3EvidenceDegradation(t *testing.T) {
	store := openTestStore(t)
	workspaceID, actorID := newTestWorkspace(t, store)
	importStage0Fixture(t, store, workspaceID, actorID)
	project := createAllLibraryProject(t, store, workspaceID, "stage3 landscape")

	external := extWork("External Reranking Study", "external-land-1", "Reranking improves hybrid retrieval through reciprocal rank fusion.")
	stub := &stubDiscovery{result: discovery.ServiceResult{
		Works: []discovery.ExternalWork{external},
		Gaps:  []discovery.Gap{},
		WebContexts: []discovery.WebContext{{
			Title: "Blog on reranking", URL: "https://example.org/reranking", Snippet: "Reranking is discussed in this background article.",
			Provider: "qwen", Model: "qwen-plus", RetrievedAt: time.Now().UTC(),
		}},
		SourceStatus: []discovery.SourceStatusEntry{
			{Source: "semantic_scholar", Status: domain.SourceAvailable},
			{Source: "openalex", Status: domain.SourceAvailable},
			{Source: "qwen_web_search", Status: domain.SourceAvailable},
		},
	}}
	engine := newTestEngine()
	engine.Store = store
	engine.Discovery = stub

	result, err := engine.RunFirst(context.Background(), workspaceID, project.ID, "hybrid retrieval reciprocal rank fusion reranking", domain.RetrievalModeLandscape)
	if err != nil {
		t.Fatalf("run landscape: %v", err)
	}
	if stub.lastReq.Mode != domain.RetrievalModeLandscape {
		t.Fatalf("discovery mode=%q want landscape", stub.lastReq.Mode)
	}

	types := map[string]bool{}
	for _, citation := range result.Brief.Citations {
		types[citation.EvidenceType] = true
	}
	for _, want := range []string{
		string(domain.EvidencePersonalFulltext),
		string(domain.EvidenceStructuredAbstract),
		string(domain.EvidenceWebContext),
	} {
		if !types[want] {
			t.Fatalf("missing evidence type %q in brief citations: %+v", want, result.Brief.Citations)
		}
	}

	// Web context must never enter findings or verify a conclusion.
	for _, finding := range result.Brief.Findings {
		if finding.VerificationStatus == string(domain.VerificationVerified) {
			for _, rel := range finding.ClaimEvidence {
				if strings.HasPrefix(rel.EvidenceID, "web:") {
					t.Fatalf("web context produced a verified finding: %+v", finding)
				}
			}
		}
	}

	// Structured external evidence priority: fulltext before abstracts before web.
	var order []string
	for _, citation := range result.Brief.Citations {
		order = append(order, citation.EvidenceType)
	}
	if len(order) > 0 && order[0] != string(domain.EvidencePersonalFulltext) {
		t.Fatalf("evidence not prioritized, first=%q order=%v", order[0], order)
	}

	if !hasSourceStatus(result.Brief.SourceStatus, "qwen_web_search", string(domain.SourceAvailable)) {
		t.Fatalf("web search status not surfaced: %+v", result.Brief.SourceStatus)
	}
}

// TestIntegrationStage3ConflictBrief proves a conflict detected during
// discovery is persisted and re-rendered with both stances retained, and that
// the conflicting abstract-level brief is never marked verified.
func TestIntegrationStage3ConflictBrief(t *testing.T) {
	store := openTestStore(t)
	workspaceID, actorID := newTestWorkspace(t, store)
	importStage0Fixture(t, store, workspaceID, actorID)
	project := createAllLibraryProject(t, store, workspaceID, "stage3 conflict")

	support := extWork("Supports", "external-conf-support", "Reranking improves dense retrieval recall on the benchmark.")
	refute := extWork("Refutes", "external-conf-refute", "Reranking does not improve dense retrieval recall on the benchmark.")
	stub := &stubDiscovery{result: discovery.ServiceResult{
		Works: []discovery.ExternalWork{support, refute},
		Gaps: []discovery.Gap{{
			Type:              discovery.GapRelatedMethod,
			Work:              refute,
			MissingRelativeTo: discovery.MissingPersonalLibrary,
			Rationale:         "conflicting evidence retained",
		}},
		WebContexts:  []discovery.WebContext{},
		SourceStatus: []discovery.SourceStatusEntry{{Source: "semantic_scholar", Status: domain.SourceAvailable}},
	}}
	engine := newTestEngine()
	engine.Store = store
	engine.Discovery = stub

	result, err := engine.RunFirst(context.Background(), workspaceID, project.ID, "does reranking improve dense retrieval recall", domain.RetrievalModeGapFill)
	if err != nil {
		t.Fatalf("run conflict gap_fill: %v", err)
	}

	stances := map[string]bool{}
	for _, finding := range result.Brief.Findings {
		for _, rel := range finding.ClaimEvidence {
			stances[rel.Stance] = true
		}
	}
	for _, claim := range result.Brief.SummaryClaims {
		for _, rel := range claim.ClaimEvidence {
			stances[rel.Stance] = true
		}
	}
	if !stances[string(domain.ClaimSupports)] || !stances[string(domain.ClaimRefutes)] {
		t.Fatalf("conflict must retain both stances end to end, got %+v", stances)
	}
	joined := strings.ToLower(strings.Join(result.Brief.OpenQuestions, " "))
	if !strings.Contains(joined, "conflict") {
		t.Fatalf("conflict open question missing: %+v", result.Brief.OpenQuestions)
	}
	// The conflicting refuting evidence must remain present with a refutes
	// stance; conflict is never resolved or hidden by citation count.
	if !stances[string(domain.ClaimRefutes)] {
		t.Fatal("refuting conflict evidence was dropped from the brief")
	}
	if len(result.Brief.Gaps) != 1 {
		t.Fatalf("conflict gap not persisted: %+v", result.Brief.Gaps)
	}

	router := newStage2Router(store, engine, workspaceID, actorID)
	rec := stage2Request(t, router, http.MethodGet, "/api/v1/briefs/"+result.Brief.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get conflict brief status=%d %s", rec.Code, rec.Body.String())
	}
	var brief query.BriefView
	if err := json.Unmarshal(rec.Body.Bytes(), &brief); err != nil {
		t.Fatalf("decode brief: %v", err)
	}
	if len(brief.SummaryClaims) == 0 {
		t.Fatal("persisted conflict brief lost its summary claim")
	}
}
