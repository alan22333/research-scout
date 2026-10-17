package query

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"research-scout/internal/discovery"
	"research-scout/internal/domain"
	"research-scout/internal/embedding"
	"research-scout/internal/planner"
	"research-scout/internal/retrieval"
	"research-scout/internal/storage"
)

type EngineConfig struct {
	PlannerVersion         string
	RetrievalConfigVersion string
	RerankVersion          string
	PromptVersion          string
}

type Engine struct {
	Store     *storage.Store
	Config    EngineConfig
	Stub      embedding.Stub
	Reranker  retrieval.RerankProvider
	Discovery DiscoveryRunner
}

type SourceStatus struct {
	Source         string  `json:"source"`
	Status         string  `json:"status"`
	DegradedReason *string `json:"degraded_reason,omitempty"`
}

// DefaultSourceStatuses returns the sources represented by a Stage 2 turn.
// External discovery remains skipped until Stage 3.
func DefaultSourceStatuses() []SourceStatus {
	return []SourceStatus{
		{Source: "zotero", Status: "available"},
		{Source: "semantic_scholar", Status: "skipped"},
		{Source: "openalex", Status: "skipped"},
		{Source: "qwen_web_search", Status: "skipped"},
	}
}

// sourceStatusesForRerank records the actual rerank path, including the
// deterministic fallback when no provider is configured or the provider fails.
func sourceStatusesForRerank(outcome retrieval.RerankOutcome) []SourceStatus {
	statuses := DefaultSourceStatuses()
	status := SourceStatus{Source: "rerank", Status: "available"}
	if !outcome.Used {
		reason := strings.TrimSpace(outcome.DegradedReason)
		if reason == "" {
			reason = "rerank_provider_unavailable"
		}
		status.Status = "degraded"
		status.DegradedReason = &reason
	}
	return append(statuses, status)
}

type SessionView struct {
	storage.Session
	CurrentTurnID string         `json:"current_turn_id,omitempty"`
	Mode          string         `json:"mode"`
	EventsURL     string         `json:"events_url"`
	SourceStatus  []SourceStatus `json:"source_status"`
}

type RunResult struct {
	Session SessionView    `json:"session"`
	Turn    storage.Turn   `json:"turn"`
	Brief   BriefView      `json:"brief"`
	Trace   RetrievalTrace `json:"-"`
}

// RetrievalTrace is an internal, non-API trace used by the offline evaluator.
// It is kept separate from BriefView so traces never leak through HTTP
// responses or become user-visible reasoning.
type RetrievalTrace struct {
	CandidateDocuments  []string `json:"-"`
	CandidateChunks     []string `json:"-"`
	PreRerankDocuments  []string `json:"-"`
	PreRerankChunks     []string `json:"-"`
	PostRerankDocuments []string `json:"-"`
	PostRerankChunks    []string `json:"-"`
	SelectedDocuments   []string `json:"-"`
	SelectedChunks      []string `json:"-"`
}

type BriefView struct {
	ID                     string           `json:"id"`
	SessionID              string           `json:"session_id"`
	TurnID                 string           `json:"turn_id"`
	SchemaVersion          string           `json:"schema_version"`
	IndexEpochID           string           `json:"index_epoch_id"`
	RetrievalConfigVersion string           `json:"retrieval_config_version"`
	PromptVersion          string           `json:"prompt_version"`
	Summary                string           `json:"summary"`
	SummaryClaims          []ClaimView      `json:"summary_claims"`
	Findings               []FindingView    `json:"findings"`
	MethodTimeline         []map[string]any `json:"method_timeline"`
	Chronology             []map[string]any `json:"chronology"`
	Gaps                   []GapView        `json:"gaps"`
	OpenQuestions          []string         `json:"open_questions"`
	Citations              []EvidenceView   `json:"citations"`
	SourceStatus           []SourceStatus   `json:"source_status"`
	CreatedAt              time.Time        `json:"created_at"`
}

type ClaimView struct {
	ClaimID            string              `json:"claim_id"`
	ClaimType          string              `json:"claim_type"`
	Text               string              `json:"text"`
	SupportLevel       string              `json:"support_level"`
	Confidence         float64             `json:"confidence"`
	VerificationStatus string              `json:"verification_status"`
	ClaimEvidence      []ClaimEvidenceView `json:"claim_evidence"`
}

type FindingView struct {
	ClaimID            string              `json:"claim_id"`
	ClaimType          string              `json:"claim_type"`
	Claim              string              `json:"claim"`
	SupportLevel       string              `json:"support_level"`
	Confidence         float64             `json:"confidence"`
	VerificationStatus string              `json:"verification_status"`
	EvidenceIDs        []string            `json:"evidence_ids"`
	ClaimEvidence      []ClaimEvidenceView `json:"claim_evidence"`
}

type ClaimEvidenceView struct {
	EvidenceID   string         `json:"evidence_id"`
	Stance       string         `json:"stance"`
	SupportScore float64        `json:"support_score"`
	EvidenceSpan *string        `json:"evidence_span,omitempty"`
	Locator      map[string]any `json:"locator"`
}

type EvidenceView struct {
	ID                string         `json:"id"`
	BriefID           string         `json:"brief_id"`
	IndexEpochID      string         `json:"index_epoch_id"`
	EvidenceType      string         `json:"evidence_type"`
	Title             string         `json:"title"`
	Quote             *string        `json:"quote,omitempty"`
	URL               *string        `json:"url,omitempty"`
	DocumentID        *string        `json:"document_id,omitempty"`
	DocumentVersionID *string        `json:"document_version_id,omitempty"`
	ChunkID           *string        `json:"chunk_id,omitempty"`
	ExternalWorkID    *string        `json:"external_work_id,omitempty"`
	Locator           map[string]any `json:"locator"`
	Score             float64        `json:"score"`
	RetrievedAt       time.Time      `json:"retrieved_at"`
}

type GapView struct {
	ID                string         `json:"id"`
	GapType           string         `json:"gap_type"`
	MissingRelativeTo string         `json:"missing_relative_to"`
	ExternalWorkID    *string        `json:"external_work_id,omitempty"`
	DocumentID        *string        `json:"document_id,omitempty"`
	Explanation       string         `json:"explanation"`
	Score             float64        `json:"score"`
	Rationale         map[string]any `json:"rationale,omitempty"`
}

type evidenceRow struct {
	view  EvidenceView
	chunk retrievedChunk
}

type retrievedChunk struct {
	ChunkID           string
	DocumentID        string
	DocumentVersionID string
	ExternalWorkID    string
	URL               string
	Provider          string
	Model             string
	Title             string
	Content           string
	Section           string
	PageStart         *int
	PageEnd           *int
	CharStart         int
	CharEnd           int
	EvidenceType      domain.EvidenceType
	Score             float64
	RetrievedAt       time.Time
}

func (e *Engine) RunFirst(ctx context.Context, workspaceID, projectID, question string, mode domain.RetrievalMode) (RunResult, error) {
	if strings.TrimSpace(question) == "" {
		return RunResult{}, fmt.Errorf("question is required")
	}
	epochID, err := e.Store.ActiveEpochID(ctx, workspaceID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return RunResult{}, fmt.Errorf("index_not_ready")
		}
		return RunResult{}, err
	}
	project, err := e.Store.GetProject(ctx, workspaceID, projectID)
	if err != nil {
		return RunResult{}, err
	}
	if mode == "" {
		mode = domain.RetrievalMode(project.DefaultMode)
	}
	if mode == "" {
		mode = domain.RetrievalModeMyLibrary
	}
	sessionID := storage.StableID("session", workspaceID, projectID, question, fmt.Sprint(time.Now().UnixNano()))
	sess, err := e.Store.CreateSession(ctx, storage.CreateSessionInput{
		ID: sessionID, WorkspaceID: workspaceID, ProjectID: projectID, Title: question, Mode: string(mode), EpochID: epochID,
	})
	if err != nil {
		return RunResult{}, err
	}
	return e.runTurn(ctx, workspaceID, sess, project, question, mode)
}

func (e *Engine) RunFollowUp(ctx context.Context, workspaceID, sessionID, question string, mode domain.RetrievalMode) (RunResult, error) {
	if strings.TrimSpace(question) == "" {
		return RunResult{}, fmt.Errorf("question is required")
	}
	sess, err := e.Store.GetSession(ctx, workspaceID, sessionID)
	if err != nil {
		return RunResult{}, err
	}
	project, err := e.Store.GetProject(ctx, workspaceID, sess.ProjectID)
	if err != nil {
		return RunResult{}, err
	}
	if mode == "" {
		mode = domain.RetrievalMode(project.DefaultMode)
	}
	if mode == "" {
		mode = domain.RetrievalModeMyLibrary
	}
	return e.runTurn(ctx, workspaceID, sess, project, question, mode)
}

func (e *Engine) runTurn(ctx context.Context, workspaceID string, sess storage.Session, project storage.Project, question string, mode domain.RetrievalMode) (RunResult, error) {
	if mode != domain.RetrievalModeMyLibrary && mode != domain.RetrievalModeGapFill && mode != domain.RetrievalModeLandscape {
		return RunResult{}, fmt.Errorf("invalid retrieval mode")
	}
	if e.Config.PlannerVersion == "" {
		e.Config.PlannerVersion = "planner-v1"
	}
	if e.Config.RetrievalConfigVersion == "" {
		e.Config.RetrievalConfigVersion = "retrieval-v1"
	}
	if e.Config.RerankVersion == "" {
		e.Config.RerankVersion = retrieval.FallbackRerankVersion
	}
	if e.Config.PromptVersion == "" {
		e.Config.PromptVersion = "brief-v1"
	}
	turnNo, err := nextTurnNo(ctx, e.Store.Pool(), workspaceID, sess.ID)
	if err != nil {
		return RunResult{}, err
	}
	turnID := storage.StableID("turn", sess.ID, fmt.Sprint(turnNo), question)
	scopeSnapshot, err := storage.JSON(map[string]any{
		"scope_mode": project.ScopeMode, "collection_ids": project.CollectionIDs, "version": project.ScopeVersion,
	})
	if err != nil {
		return RunResult{}, err
	}
	plan := planner.Plan(question)
	planJSON, err := storage.JSON(plan)
	if err != nil {
		return RunResult{}, err
	}
	turn := storage.Turn{}
	_, err = e.Store.Pool().Exec(ctx, `
		INSERT INTO query_turns
			(id,session_id,workspace_id,turn_no,question,mode,scope_snapshot_json,index_epoch_id,
			 planner_version,retrieval_config_version,prompt_version,status,phase,plan_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11,'running','planning',$12::jsonb)
	`, turnID, sess.ID, workspaceID, turnNo, question, string(mode), scopeSnapshot, sess.IndexEpochID,
		e.Config.PlannerVersion, e.Config.RetrievalConfigVersion, e.Config.PromptVersion, planJSON)
	if err != nil {
		return RunResult{}, err
	}
	turn = storage.Turn{
		ID: turnID, SessionID: sess.ID, TurnNo: turnNo, Question: question, Mode: string(mode),
		Status: "running", Phase: string(domain.PhasePlanning), IndexEpochID: sess.IndexEpochID,
		PlannerVersion: e.Config.PlannerVersion, RetrievalConfigVersion: e.Config.RetrievalConfigVersion,
		PromptVersion: e.Config.PromptVersion, CreatedAt: time.Now().UTC(),
	}
	if err := e.event(ctx, workspaceID, sess.ID, turnID, "session_accepted", map[string]any{"question": question, "mode": mode}); err != nil {
		return RunResult{}, err
	}
	if err := e.event(ctx, workspaceID, sess.ID, turnID, "phase_changed", map[string]any{"phase": domain.PhasePlanning}); err != nil {
		return RunResult{}, err
	}
	if err := e.event(ctx, workspaceID, sess.ID, turnID, "plan_updated", map[string]any{
		"plan_version": plan.Version, "intent": plan.Intent, "route": plan.Route,
		"subquery_count": len(plan.Subqueries),
	}); err != nil {
		return RunResult{}, err
	}
	if err := e.event(ctx, workspaceID, sess.ID, turnID, "phase_changed", map[string]any{"phase": domain.PhaseRetrievingLibrary, "source": "zotero"}); err != nil {
		return RunResult{}, err
	}

	chunks, trace, rerankOutcome, err := e.retrieveHybrid(ctx, workspaceID, sess.IndexEpochID, project, plan)
	if err != nil {
		_ = e.failTurn(ctx, workspaceID, turnID, err)
		return RunResult{}, err
	}

	// Stage 3 external discovery: gap_fill and landscape first run structured
	// scholarly discovery; landscape may then fall back to provider-native web
	// search when structured coverage still leaves gaps. my_library never
	// reaches this block and therefore never calls an external source.
	discoveryResult := emptyDiscoveryResult()
	if modeAllowsStructured(mode) {
		if err := e.event(ctx, workspaceID, sess.ID, turnID, "phase_changed", map[string]any{
			"phase": domain.PhaseRetrievingScholarly, "sources": []string{"semantic_scholar", "openalex"},
		}); err != nil {
			return RunResult{}, err
		}
		if modeAllowsWebSearch(mode) {
			if err := e.event(ctx, workspaceID, sess.ID, turnID, "phase_changed", map[string]any{
				"phase": domain.PhaseRetrievingWeb, "source": "qwen_web_search",
			}); err != nil {
				return RunResult{}, err
			}
		}
		var discoveryChunks []retrievedChunk
		discoveryResult, discoveryChunks = e.runDiscovery(ctx, workspaceID, mode, project, question, deidentifiedTopic(plan), chunks)
		chunks = mergeEvidence(chunks, discoveryChunks, maxDocumentsForMode(mode))
	}

	for _, c := range chunks {
		data := map[string]any{
			"chunk_id": c.ChunkID, "document_id": c.DocumentID, "evidence_type": c.EvidenceType,
		}
		if c.ExternalWorkID != "" {
			data["external_work_id"] = c.ExternalWorkID
		}
		if c.URL != "" {
			data["url"] = c.URL
		}
		if err := e.event(ctx, workspaceID, sess.ID, turnID, "evidence_ready", data); err != nil {
			return RunResult{}, err
		}
	}
	if err := e.event(ctx, workspaceID, sess.ID, turnID, "phase_changed", map[string]any{"phase": domain.PhaseRanking}); err != nil {
		return RunResult{}, err
	}
	if err := e.event(ctx, workspaceID, sess.ID, turnID, "phase_changed", map[string]any{"phase": domain.PhaseDrafting}); err != nil {
		return RunResult{}, err
	}

	brief, err := e.persistBrief(ctx, workspaceID, sess, turn, question, chunks, rerankOutcome, discoveryResult)
	if err != nil {
		_ = e.failTurn(ctx, workspaceID, turnID, err)
		return RunResult{}, err
	}
	if err := ValidateBriefClaims(domain.Brief{
		SummaryClaims: claimViewsToDomain(brief.SummaryClaims),
		Findings:      findingViewsToDomain(brief.Findings),
	}); err != nil {
		_ = e.failTurn(ctx, workspaceID, turnID, err)
		return RunResult{}, err
	}
	if _, err := e.Store.Pool().Exec(ctx, `
		UPDATE query_turns SET status='completed', phase='completed', completed_at=now() WHERE id=$1 AND workspace_id=$2
	`, turnID, workspaceID); err != nil {
		return RunResult{}, err
	}
	turn.Status = "completed"
	turn.Phase = string(domain.PhaseCompleted)
	if err := e.event(ctx, workspaceID, sess.ID, turnID, "brief_ready", map[string]any{"brief_id": brief.ID}); err != nil {
		return RunResult{}, err
	}
	if err := e.event(ctx, workspaceID, sess.ID, turnID, "phase_changed", map[string]any{"phase": domain.PhaseCompleted}); err != nil {
		return RunResult{}, err
	}
	if _, err := e.Store.Pool().Exec(ctx, `UPDATE query_sessions SET status='completed', updated_at=now() WHERE id=$1 AND workspace_id=$2`, sess.ID, workspaceID); err != nil {
		return RunResult{}, err
	}
	sess.Status = "completed"
	sess.Phase = string(domain.PhaseCompleted)
	view := SessionView{
		Session: sess, CurrentTurnID: turnID, Mode: string(mode), EventsURL: "/api/v1/sessions/" + sess.ID + "/events",
		SourceStatus: sourceStatusesForTurn(rerankOutcome, discoveryResult.SourceStatus),
	}
	return RunResult{Session: view, Turn: turn, Brief: brief, Trace: trace}, nil
}

func nextTurnNo(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, workspaceID, sessionID string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT COALESCE(max(turn_no),0)+1 FROM query_turns WHERE workspace_id=$1 AND session_id=$2`, workspaceID, sessionID).Scan(&n)
	return n, err
}

// retrieveHybrid runs the planned subqueries through lexical (FTS) and vector
// candidate generation, fuses them with weighted RRF, then applies the
// selection caps. Every candidate is filtered by workspace, active epoch,
// active document version, scope, lifecycle, and index status inside the SQL
// projections so no cross-tenant or stale data can enter the candidate pool.
func (e *Engine) retrieveHybrid(ctx context.Context, workspaceID, epochID string, project storage.Project, plan planner.QueryPlan) ([]retrievedChunk, RetrievalTrace, retrieval.RerankOutcome, error) {
	subqueries := plan.Subqueries
	if len(subqueries) == 0 {
		subqueries = []planner.Subquery{{Query: strings.TrimSpace(plan.Question), Weight: 1, Purpose: "fallback"}}
	}
	if len(subqueries) > 5 {
		subqueries = subqueries[:5]
	}

	lexicalLists := make([]retrieval.RankedList, 0, len(subqueries))
	vectorLists := make([]retrieval.RankedList, 0, len(subqueries))
	for _, sub := range subqueries {
		query := strings.TrimSpace(sub.Query)
		if query == "" {
			continue
		}
		weight := sub.Weight
		if weight <= 0 {
			weight = 1
		}
		lexical, err := e.retrieveLexical(ctx, workspaceID, epochID, project, query)
		if err != nil {
			return nil, RetrievalTrace{}, retrieval.RerankOutcome{}, err
		}
		if len(lexical) > 0 {
			lexicalLists = append(lexicalLists, retrieval.RankedList{QueryWeight: weight, SourceWeight: 1.0, Source: retrieval.SourceLexical, Candidates: lexical})
		}
		vector, err := e.retrieveVector(ctx, workspaceID, epochID, project, query)
		if err != nil {
			return nil, RetrievalTrace{}, retrieval.RerankOutcome{}, err
		}
		if len(vector) > 0 {
			vectorLists = append(vectorLists, retrieval.RankedList{QueryWeight: weight, SourceWeight: 1.0, Source: retrieval.SourceVector, Candidates: vector})
		}
	}

	lists := append(append([]retrieval.RankedList{}, lexicalLists...), vectorLists...)
	fused := retrieval.FuseRRF(lists, retrieval.DefaultRRFK)
	reranked, rerankOutcome := retrieval.RerankOrFallback(ctx, e.Reranker, plan.Question, fused)
	if strings.TrimSpace(rerankOutcome.Version) == "" {
		rerankOutcome.Version = e.Config.RerankVersion
	}
	if strings.TrimSpace(rerankOutcome.Version) == "" {
		rerankOutcome.Version = retrieval.FallbackRerankVersion
	}
	selected := retrieval.Select(reranked, retrieval.SelectionConfig{
		MaxChunks: 12, MaxDocuments: 6, PerDocumentCap: 2, TokenBudget: 12000,
	})
	trace := traceFromCandidates(fused, reranked, selected)
	out := make([]retrievedChunk, 0, len(selected))
	for _, c := range selected {
		out = append(out, retrievedChunk{
			ChunkID: c.ChunkID, DocumentID: c.DocumentID, DocumentVersionID: c.DocumentVersionID,
			Title: c.Title, Content: c.Content, Section: c.Section,
			PageStart: c.PageStart, PageEnd: c.PageEnd, CharStart: c.CharStart, CharEnd: c.CharEnd,
			EvidenceType: c.EvidenceType, Score: c.FusedScore,
		})
	}
	return out, trace, rerankOutcome, nil
}

func traceFromCandidates(fused, reranked, selected []retrieval.Candidate) RetrievalTrace {
	fusedDocs, fusedChunks := rankedIDs(fused)
	rerankedDocs, rerankedChunks := rankedIDs(reranked)
	selectedDocs, selectedChunks := rankedIDs(selected)
	return RetrievalTrace{
		CandidateDocuments:  fusedDocs,
		CandidateChunks:     fusedChunks,
		PreRerankDocuments:  fusedDocs,
		PreRerankChunks:     fusedChunks,
		PostRerankDocuments: rerankedDocs,
		PostRerankChunks:    rerankedChunks,
		SelectedDocuments:   selectedDocs,
		SelectedChunks:      selectedChunks,
	}
}

func rankedIDs(candidates []retrieval.Candidate) ([]string, []string) {
	documents := make([]string, 0, len(candidates))
	chunks := make([]string, 0, len(candidates))
	seenDocuments := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.DocumentID != "" && !seenDocuments[candidate.DocumentID] {
			seenDocuments[candidate.DocumentID] = true
			documents = append(documents, candidate.DocumentID)
		}
		if candidate.ChunkID != "" {
			chunks = append(chunks, candidate.ChunkID)
		}
	}
	return documents, chunks
}

const lexicalCandidateLimit = 40
const vectorCandidateLimit = 40

// The deterministic and future providers have different score distributions.
// These floors keep unrelated documents out of the candidate pool; they are
// part of the retrieval configuration and must be revisited before production
// evaluation with a real embedding/rerank provider.
const lexicalMinSimilarity = 0.30
const vectorMinSimilarity = 0.05

// retrieveLexical returns the top lexical candidates for one subquery.
func (e *Engine) retrieveLexical(ctx context.Context, workspaceID, epochID string, project storage.Project, query string) ([]retrieval.Candidate, error) {
	scopeMode := project.ScopeMode
	scopeIDs := project.CollectionIDs
	scopeJSON := "[]"
	if scopeMode != "all_library" {
		var err error
		scopeJSON, err = storage.JSON(scopeIDs)
		if err != nil {
			return nil, err
		}
	}
	rows, err := e.Store.Pool().Query(ctx, `
		SELECT c.id, c.document_id, c.document_version_id, d.title, c.content, c.section,
			c.page_start, c.page_end, c.char_start, c.char_end, d.evidence_type, c.token_count,
			(ts_rank_cd(c.search_tsv, plainto_tsquery('english',$5)) +
			 similarity(c.content,$5) * 0.25) AS score
		FROM chunks c
		JOIN index_epoch_documents ied
			ON ied.epoch_id=c.index_epoch_id
			AND ied.document_id=c.document_id
			AND ied.document_version_id=c.document_version_id
		JOIN documents d ON d.id=c.document_id AND d.workspace_id=c.workspace_id
		WHERE c.workspace_id=$1
			AND c.index_epoch_id=$2
			AND d.active_version_id=c.document_version_id
			AND d.removed_at IS NULL
			AND d.index_status='indexed'
			AND d.evidence_type IN ('personal_fulltext','structured_abstract')
			AND (
				c.search_tsv @@ plainto_tsquery('english',$5)
				OR similarity(c.content,$5) >= $6
			)
			AND (
				$3='all_library'
				OR EXISTS (
					SELECT 1 FROM document_collections dc
					WHERE dc.workspace_id=c.workspace_id
						AND dc.document_id=c.document_id
						AND dc.collection_id IN (SELECT jsonb_array_elements_text($4::jsonb))
				)
			)
		ORDER BY score DESC, c.document_id, c.ordinal
		LIMIT $7
	`, workspaceID, epochID, scopeMode, scopeJSON, query, lexicalMinSimilarity, lexicalCandidateLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []retrieval.Candidate
	for rows.Next() {
		var c retrieval.Candidate
		var evidenceType string
		var score float64
		if err := rows.Scan(&c.ChunkID, &c.DocumentID, &c.DocumentVersionID, &c.Title, &c.Content, &c.Section,
			&c.PageStart, &c.PageEnd, &c.CharStart, &c.CharEnd, &evidenceType, &c.TokenCount, &score); err != nil {
			return nil, err
		}
		c.EvidenceType = domain.EvidenceType(evidenceType)
		c.Source = retrieval.SourceLexical
		if score <= 0 {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// retrieveVector embeds the subquery and ranks in-scope chunks by cosine
// similarity using the deterministic local embedding stub. The candidate pool is
// bounded to the active epoch and scope, and never performs network I/O.
func (e *Engine) retrieveVector(ctx context.Context, workspaceID, epochID string, project storage.Project, query string) ([]retrieval.Candidate, error) {
	queryVector := e.Stub.Vector(query)
	scopeMode := project.ScopeMode
	scopeIDs := project.CollectionIDs
	scopeJSON := "[]"
	if scopeMode != "all_library" {
		var err error
		scopeJSON, err = storage.JSON(scopeIDs)
		if err != nil {
			return nil, err
		}
	}
	rows, err := e.Store.Pool().Query(ctx, `
		SELECT c.id, c.document_id, c.document_version_id, d.title, c.content, c.section,
			c.page_start, c.page_end, c.char_start, c.char_end, d.evidence_type, c.token_count,
			c.embedding::text
		FROM chunks c
		JOIN index_epoch_documents ied
			ON ied.epoch_id=c.index_epoch_id
			AND ied.document_id=c.document_id
			AND ied.document_version_id=c.document_version_id
		JOIN documents d ON d.id=c.document_id AND d.workspace_id=c.workspace_id
		WHERE c.workspace_id=$1
			AND c.index_epoch_id=$2
			AND d.active_version_id=c.document_version_id
			AND d.removed_at IS NULL
			AND d.index_status='indexed'
			AND d.evidence_type IN ('personal_fulltext','structured_abstract')
			AND (
				$3='all_library'
				OR EXISTS (
					SELECT 1 FROM document_collections dc
					WHERE dc.workspace_id=c.workspace_id
						AND dc.document_id=c.document_id
						AND dc.collection_id IN (SELECT jsonb_array_elements_text($4::jsonb))
				)
			)
	`, workspaceID, epochID, scopeMode, scopeJSON)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pool []scoredCandidate
	for rows.Next() {
		var c retrieval.Candidate
		var evidenceType string
		var embeddingText string
		if err := rows.Scan(&c.ChunkID, &c.DocumentID, &c.DocumentVersionID, &c.Title, &c.Content, &c.Section,
			&c.PageStart, &c.PageEnd, &c.CharStart, &c.CharEnd, &evidenceType, &c.TokenCount, &embeddingText); err != nil {
			return nil, err
		}
		c.EvidenceType = domain.EvidenceType(evidenceType)
		c.Source = retrieval.SourceVector
		vec, ok := parseVectorText(embeddingText)
		if !ok {
			continue
		}
		similarity := cosine(queryVector, vec)
		if similarity < vectorMinSimilarity {
			continue
		}
		pool = append(pool, scoredCandidate{candidate: c, score: similarity})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].score != pool[j].score {
			return pool[i].score > pool[j].score
		}
		if pool[i].candidate.DocumentID != pool[j].candidate.DocumentID {
			return pool[i].candidate.DocumentID < pool[j].candidate.DocumentID
		}
		return pool[i].candidate.ChunkID < pool[j].candidate.ChunkID
	})
	if len(pool) > vectorCandidateLimit {
		pool = pool[:vectorCandidateLimit]
	}
	out := make([]retrieval.Candidate, 0, len(pool))
	for _, item := range pool {
		out = append(out, item.candidate)
	}
	return out, nil
}

// scoredCandidate pairs a retrieval candidate with its vector similarity.
type scoredCandidate struct {
	candidate retrieval.Candidate
	score     float64
}

// parseVectorText parses the pgvector text representation, e.g. "[0.1,-0.2]".
func parseVectorText(raw string) ([]float32, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '[' || raw[len(raw)-1] != ']' {
		return nil, false
	}
	body := raw[1 : len(raw)-1]
	if strings.TrimSpace(body) == "" {
		return nil, false
	}
	parts := strings.Split(body, ",")
	out := make([]float32, 0, len(parts))
	for _, part := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(part), 32)
		if err != nil {
			return nil, false
		}
		out = append(out, float32(v))
	}
	return out, true
}

// cosine returns the cosine similarity of two equal-length vectors. A mismatch
// or zero vector yields 0 so it can never outrank a real match.
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func (e *Engine) persistBrief(ctx context.Context, workspaceID string, sess storage.Session, turn storage.Turn, question string, chunks []retrievedChunk, rerankOutcome retrieval.RerankOutcome, discoveryResult discovery.ServiceResult) (BriefView, error) {
	tx, err := e.Store.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return BriefView{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	briefID := storage.StableID("brief", turn.ID)
	now := time.Now().UTC()
	sourceStatus := sourceStatusesForTurn(rerankOutcome, discoveryResult.SourceStatus)
	sourceStatusJSON, err := storage.JSON(sourceStatus)
	if err != nil {
		return BriefView{}, err
	}

	// Apply the grounding policy. Evidence identities are derived from the
	// brief id + chunk id + ordinal so generation and persistence agree. Web
	// context is persisted as low-confidence background, never as a finding.
	content := buildBriefContent(briefID, question, chunks)

	evidenceViews := make([]EvidenceView, 0, len(chunks))
	evidenceRows := make([]evidenceRow, 0, len(chunks))
	for idx, c := range chunks {
		if !persistableEvidenceType(c.EvidenceType) {
			continue
		}
		evidenceID := evidenceIDFor(briefID, c.ChunkID, idx)
		locator := locatorFor(c)
		locator["index_epoch_id"] = turn.IndexEpochID
		quote := truncate(c.Content, 360)
		view := EvidenceView{
			ID: evidenceID, BriefID: briefID, IndexEpochID: turn.IndexEpochID, EvidenceType: string(c.EvidenceType),
			Title: c.Title, Quote: stringPtr(quote), Locator: locator, Score: c.Score, RetrievedAt: now,
		}
		if c.DocumentID != "" {
			view.DocumentID = stringPtr(c.DocumentID)
		}
		if c.DocumentVersionID != "" {
			view.DocumentVersionID = stringPtr(c.DocumentVersionID)
		}
		if c.ChunkID != "" {
			view.ChunkID = stringPtr(c.ChunkID)
		}
		if c.ExternalWorkID != "" {
			view.ExternalWorkID = stringPtr(c.ExternalWorkID)
		}
		if c.URL != "" {
			view.URL = stringPtr(c.URL)
		}
		evidenceViews = append(evidenceViews, view)
		evidenceRows = append(evidenceRows, evidenceRow{view: view, chunk: c})
	}

	gaps := gapsFromDiscovery(briefID, discoveryResult.Gaps)
	for i := range gaps {
		if gaps[i].ID == "" {
			gaps[i].ID = storage.StableID("gap", briefID, gaps[i].GapType, fmt.Sprint(i))
		}
	}

	findingsJSON, _ := storage.JSON(content.Findings)
	openJSON, _ := storage.JSON(content.OpenQuestions)
	if _, err := tx.Exec(ctx, `
		INSERT INTO briefs
			(id,session_id,turn_id,workspace_id,schema_version,summary,findings_json,method_timeline_json,
			 chronology_json,open_questions_json,source_status_json,index_epoch_id,retrieval_config_version,prompt_version,created_at)
		VALUES ($1,$2,$3,$4,'1.1',$5,$6::jsonb,'[]'::jsonb,'[]'::jsonb,$7::jsonb,$8::jsonb,$9,$10,$11,$12)
		ON CONFLICT (turn_id) DO NOTHING
	`, briefID, sess.ID, turn.ID, workspaceID, content.Summary, findingsJSON, openJSON, sourceStatusJSON,
		turn.IndexEpochID, turn.RetrievalConfigVersion, turn.PromptVersion, now); err != nil {
		return BriefView{}, err
	}
	for _, row := range evidenceRows {
		locatorJSON, _ := storage.JSON(row.view.Locator)
		sourceMetaJSON, _ := storage.JSON(evidenceSourceMeta(row.chunk))
		quote := ""
		if row.view.Quote != nil {
			quote = *row.view.Quote
		}
		docID := nullablePtr(row.view.DocumentID)
		dvID := nullablePtr(row.view.DocumentVersionID)
		chunkID := nullablePtr(row.view.ChunkID)
		externalID := nullablePtr(row.view.ExternalWorkID)
		url := ""
		if row.view.URL != nil {
			url = *row.view.URL
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO evidence
				(id,brief_id,workspace_id,index_epoch_id,evidence_type,document_id,document_version_id,chunk_id,
				 external_work_id,title,quote,url,locator_json,score,retrieved_at,source_meta_json)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14,$15,$16::jsonb)
			ON CONFLICT (id) DO NOTHING
		`, row.view.ID, briefID, workspaceID, turn.IndexEpochID, row.view.EvidenceType, docID, dvID,
			chunkID, externalID, row.view.Title, quote, url, locatorJSON, row.view.Score, now, sourceMetaJSON); err != nil {
			return BriefView{}, err
		}
	}
	if err := e.persistClaims(ctx, tx, workspaceID, briefID, content.SummaryClaims, content.Findings); err != nil {
		return BriefView{}, err
	}
	if err := persistGapsTx(ctx, tx, workspaceID, briefID, gaps); err != nil {
		return BriefView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BriefView{}, err
	}

	return BriefView{
		ID: briefID, SessionID: sess.ID, TurnID: turn.ID, SchemaVersion: "1.1", IndexEpochID: turn.IndexEpochID,
		RetrievalConfigVersion: turn.RetrievalConfigVersion, PromptVersion: turn.PromptVersion,
		Summary: content.Summary, SummaryClaims: content.SummaryClaims, Findings: content.Findings,
		MethodTimeline: []map[string]any{}, Chronology: []map[string]any{}, Gaps: gaps,
		OpenQuestions: content.OpenQuestions, Citations: evidenceViews, SourceStatus: sourceStatus, CreatedAt: now,
	}, nil
}

func persistableEvidenceType(t domain.EvidenceType) bool {
	return t == domain.EvidencePersonalFulltext || t == domain.EvidenceStructuredAbstract || t == domain.EvidenceWebContext
}

// evidenceSourceMeta records provenance for one persisted evidence row without
// leaking private content or provider secrets.
func evidenceSourceMeta(c retrievedChunk) map[string]any {
	meta := map[string]any{"provider": "zotero_web_api"}
	switch c.EvidenceType {
	case domain.EvidenceStructuredAbstract:
		meta["provider"] = "structured_discovery"
		if c.Provider != "" {
			meta["source"] = c.Provider
		}
		if c.ExternalWorkID != "" {
			meta["external_work_id"] = c.ExternalWorkID
		}
	case domain.EvidenceWebContext:
		meta["provider"] = "qwen_web_search"
		if c.Provider != "" {
			meta["provider"] = c.Provider
		}
		if c.Model != "" {
			meta["model"] = c.Model
		}
		if c.URL != "" {
			meta["url"] = c.URL
		}
	}
	return meta
}

// persistGapsTx writes discovery gaps inside the brief transaction. Repeat
// writes are idempotent on the stable gap id.
func persistGapsTx(ctx context.Context, tx pgx.Tx, workspaceID, briefID string, gaps []GapView) error {
	for i, g := range gaps {
		id := g.ID
		if id == "" {
			id = storage.StableID("gap", briefID, g.GapType, fmt.Sprint(i))
		}
		rationale := g.Rationale
		if rationale == nil {
			rationale = map[string]any{}
		}
		rationaleJSON, _ := storage.JSON(rationale)
		externalWorkID := ""
		if g.ExternalWorkID != nil {
			externalWorkID = *g.ExternalWorkID
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO gaps
				(id,brief_id,workspace_id,gap_type,missing_relative_to,explanation,external_work_id,rationale_json,score)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9)
			ON CONFLICT (id) DO UPDATE SET
				explanation = EXCLUDED.explanation,
				external_work_id = EXCLUDED.external_work_id,
				rationale_json = EXCLUDED.rationale_json,
				score = EXCLUDED.score
		`, id, briefID, workspaceID, g.GapType, g.MissingRelativeTo, g.Explanation, externalWorkID, rationaleJSON, g.Score); err != nil {
			return err
		}
	}
	return nil
}

func nullablePtr(v *string) any {
	if v == nil || *v == "" {
		return nil
	}
	return *v
}

// gapsFromDiscovery maps detected discovery gaps onto the persisted view.
func gapsFromDiscovery(briefID string, in []discovery.Gap) []GapView {
	out := make([]GapView, 0, len(in))
	for i, g := range in {
		workID := g.Work.ID
		view := GapView{
			ID:                storage.StableID("gap", briefID, string(g.Type), workID, fmt.Sprint(i)),
			GapType:           string(g.Type),
			MissingRelativeTo: string(g.MissingRelativeTo),
			Explanation:       g.Rationale,
			Score:             gapScore(g),
			Rationale: map[string]any{
				"work_title":     g.Work.Title,
				"work_year":      g.Work.Year,
				"citation_count": g.Work.CitationCount,
			},
		}
		if workID != "" {
			id := workID
			view.ExternalWorkID = &id
		}
		out = append(out, view)
	}
	return out
}

// gapScore is a deterministic, non-authoritative ordering hint. It never
// substitutes for the rationale fields and is never used to override stance.
func gapScore(g discovery.Gap) float64 {
	base := 0.5
	switch g.Type {
	case discovery.GapCanonical:
		base = 0.9
	case discovery.GapLatestProgress:
		base = 0.6
	}
	return base
}

func (e *Engine) persistClaims(ctx context.Context, tx pgx.Tx, workspaceID, briefID string, summaryClaims []ClaimView, findings []FindingView) error {
	for _, claim := range summaryClaims {
		if err := e.persistClaim(ctx, tx, workspaceID, briefID, claim.ClaimID, 1, claim.ClaimType, claim.Text, claim.SupportLevel, claim.Confidence, claim.VerificationStatus, claim.ClaimEvidence); err != nil {
			return err
		}
	}
	for idx, finding := range findings {
		if err := e.persistClaim(ctx, tx, workspaceID, briefID, finding.ClaimID, idx+1, finding.ClaimType, finding.Claim, finding.SupportLevel, finding.Confidence, finding.VerificationStatus, finding.ClaimEvidence); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) persistClaim(ctx context.Context, tx pgx.Tx, workspaceID, briefID, claimID string, claimNo int, claimType, text, supportLevel string, confidence float64, verification string, relations []ClaimEvidenceView) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO claims
			(id,brief_id,workspace_id,claim_no,claim_type,text,support_level,confidence,verification_status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET text=EXCLUDED.text, support_level=EXCLUDED.support_level,
			confidence=EXCLUDED.confidence, verification_status=EXCLUDED.verification_status
	`, claimID, briefID, workspaceID, claimNo, claimType, text, supportLevel, confidence, verification); err != nil {
		return err
	}
	for _, rel := range relations {
		span := ""
		if rel.EvidenceSpan != nil {
			span = *rel.EvidenceSpan
		}
		locatorJSON, _ := storage.JSON(rel.Locator)
		if _, err := tx.Exec(ctx, `
			INSERT INTO claim_evidence
				(claim_id,evidence_id,workspace_id,stance,support_score,evidence_span,locator_json)
			VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb)
			ON CONFLICT (claim_id,evidence_id,stance) DO UPDATE SET support_score=EXCLUDED.support_score,
				evidence_span=EXCLUDED.evidence_span, locator_json=EXCLUDED.locator_json
		`, claimID, rel.EvidenceID, workspaceID, rel.Stance, rel.SupportScore, span, locatorJSON); err != nil {
			return err
		}
	}
	return nil
}

// event appends a durable SSE event with a monotonic per-session sequence. The
// session row is locked inside the transaction so concurrent writers cannot pick
// the same sequence and silently drop an event through the unique constraint.
func (e *Engine) event(ctx context.Context, workspaceID, sessionID, turnID, eventType string, data map[string]any) error {
	dataJSON, err := storage.JSON(data)
	if err != nil {
		return err
	}
	tx, err := e.Store.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var locked string
	if err := tx.QueryRow(ctx, `SELECT id FROM query_sessions WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspaceID, sessionID).Scan(&locked); err != nil {
		return err
	}
	var seq int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(seq),0)+1 FROM query_events WHERE workspace_id=$1 AND session_id=$2`, workspaceID, sessionID).Scan(&seq); err != nil {
		return err
	}
	id := storage.StableID("event", sessionID, fmt.Sprint(seq), eventType)
	if _, err := tx.Exec(ctx, `
		INSERT INTO query_events (id,session_id,turn_id,workspace_id,seq,type,data_json)
		VALUES ($1,$2,NULLIF($3,''),$4,$5,$6,$7::jsonb)
		ON CONFLICT (session_id, seq) DO NOTHING
	`, id, sessionID, turnID, workspaceID, seq, eventType, dataJSON); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (e *Engine) failTurn(ctx context.Context, workspaceID, turnID string, cause error) error {
	_, err := e.Store.Pool().Exec(ctx, `
		UPDATE query_turns SET status='failed', phase='failed', completed_at=now() WHERE id=$1 AND workspace_id=$2
	`, turnID, workspaceID)
	_ = cause
	return err
}

func (e *Engine) GetBrief(ctx context.Context, workspaceID, sessionID string) (BriefView, error) {
	var briefID, turnID, epochID string
	err := e.Store.Pool().QueryRow(ctx, `
		SELECT id, turn_id, index_epoch_id FROM briefs
		WHERE workspace_id=$1 AND session_id=$2
		ORDER BY created_at DESC LIMIT 1
	`, workspaceID, sessionID).Scan(&briefID, &turnID, &epochID)
	if err != nil {
		return BriefView{}, err
	}
	return e.GetBriefByID(ctx, workspaceID, briefID)
}

func (e *Engine) GetBriefByID(ctx context.Context, workspaceID, briefID string) (BriefView, error) {
	var out BriefView
	var findingsRaw, openRaw, sourceRaw []byte
	err := e.Store.Pool().QueryRow(ctx, `
		SELECT id,session_id,turn_id,schema_version,index_epoch_id,retrieval_config_version,prompt_version,
			summary,findings_json,open_questions_json,source_status_json,created_at
		FROM briefs WHERE workspace_id=$1 AND id=$2
	`, workspaceID, briefID).Scan(&out.ID, &out.SessionID, &out.TurnID, &out.SchemaVersion, &out.IndexEpochID,
		&out.RetrievalConfigVersion, &out.PromptVersion, &out.Summary, &findingsRaw, &openRaw, &sourceRaw, &out.CreatedAt)
	if err != nil {
		return BriefView{}, err
	}
	_ = json.Unmarshal(findingsRaw, &out.Findings)
	_ = json.Unmarshal(openRaw, &out.OpenQuestions)
	_ = json.Unmarshal(sourceRaw, &out.SourceStatus)
	out.MethodTimeline = []map[string]any{}
	out.Chronology = []map[string]any{}
	out.Gaps, err = e.listGaps(ctx, workspaceID, briefID)
	if err != nil {
		return BriefView{}, err
	}
	out.SummaryClaims, err = e.listClaims(ctx, workspaceID, briefID, "summary")
	if err != nil {
		return BriefView{}, err
	}
	findingsClaims, err := e.listClaims(ctx, workspaceID, briefID, "finding")
	if err != nil {
		return BriefView{}, err
	}
	out.Findings = make([]FindingView, 0, len(findingsClaims))
	for _, c := range findingsClaims {
		out.Findings = append(out.Findings, FindingView{
			ClaimID: c.ClaimID, ClaimType: c.ClaimType, Claim: c.Text, SupportLevel: c.SupportLevel,
			Confidence: c.Confidence, VerificationStatus: c.VerificationStatus,
			EvidenceIDs: evidenceIDs(c.ClaimEvidence), ClaimEvidence: c.ClaimEvidence,
		})
	}
	out.Citations, err = e.ListEvidence(ctx, workspaceID, briefID)
	if err != nil {
		return BriefView{}, err
	}
	return out, nil
}

func (e *Engine) listClaims(ctx context.Context, workspaceID, briefID, claimType string) ([]ClaimView, error) {
	rows, err := e.Store.Pool().Query(ctx, `
		SELECT id,claim_type,text,support_level,confidence,verification_status
		FROM claims WHERE workspace_id=$1 AND brief_id=$2 AND claim_type=$3
		ORDER BY claim_no,id
	`, workspaceID, briefID, claimType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClaimView
	for rows.Next() {
		var c ClaimView
		if err := rows.Scan(&c.ClaimID, &c.ClaimType, &c.Text, &c.SupportLevel, &c.Confidence, &c.VerificationStatus); err != nil {
			return nil, err
		}
		c.ClaimEvidence, err = e.listClaimEvidence(ctx, workspaceID, c.ClaimID)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (e *Engine) listClaimEvidence(ctx context.Context, workspaceID, claimID string) ([]ClaimEvidenceView, error) {
	rows, err := e.Store.Pool().Query(ctx, `
		SELECT evidence_id,stance,support_score,evidence_span,locator_json
		FROM claim_evidence WHERE workspace_id=$1 AND claim_id=$2
		ORDER BY evidence_id
	`, workspaceID, claimID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClaimEvidenceView
	for rows.Next() {
		var c ClaimEvidenceView
		var span *string
		var locatorRaw []byte
		if err := rows.Scan(&c.EvidenceID, &c.Stance, &c.SupportScore, &span, &locatorRaw); err != nil {
			return nil, err
		}
		c.EvidenceSpan = span
		c.Locator = map[string]any{}
		_ = json.Unmarshal(locatorRaw, &c.Locator)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListGaps returns the persisted, workspace-scoped gaps for a brief including
// the external work reference, rationale and score.
func (e *Engine) ListGaps(ctx context.Context, workspaceID, briefID string) ([]GapView, error) {
	return e.listGaps(ctx, workspaceID, briefID)
}

func (e *Engine) listGaps(ctx context.Context, workspaceID, briefID string) ([]GapView, error) {
	rows, err := e.Store.Pool().Query(ctx, `
		SELECT id, gap_type, missing_relative_to, explanation, NULLIF(external_work_id,''), rationale_json, score
		FROM gaps WHERE workspace_id=$1 AND brief_id=$2
		ORDER BY created_at, id
	`, workspaceID, briefID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GapView{}
	for rows.Next() {
		var g GapView
		var externalID *string
		var rationaleRaw []byte
		if err := rows.Scan(&g.ID, &g.GapType, &g.MissingRelativeTo, &g.Explanation, &externalID, &rationaleRaw, &g.Score); err != nil {
			return nil, err
		}
		g.ExternalWorkID = externalID
		g.Rationale = map[string]any{}
		_ = json.Unmarshal(rationaleRaw, &g.Rationale)
		out = append(out, g)
	}
	return out, rows.Err()
}

func (e *Engine) ListEvidence(ctx context.Context, workspaceID, briefID string) ([]EvidenceView, error) {
	rows, err := e.Store.Pool().Query(ctx, `
		SELECT id,brief_id,index_epoch_id,evidence_type,title,quote,url,document_id,document_version_id,
			chunk_id,external_work_id,locator_json,score,retrieved_at
		FROM evidence WHERE workspace_id=$1 AND brief_id=$2
		ORDER BY score DESC, id
	`, workspaceID, briefID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvidenceView
	for rows.Next() {
		var ev EvidenceView
		var quote, url, docID, dvID, chunkID, externalID *string
		var locatorRaw []byte
		if err := rows.Scan(&ev.ID, &ev.BriefID, &ev.IndexEpochID, &ev.EvidenceType, &ev.Title, &quote, &url, &docID, &dvID, &chunkID, &externalID, &locatorRaw, &ev.Score, &ev.RetrievedAt); err != nil {
			return nil, err
		}
		ev.Quote, ev.URL, ev.DocumentID, ev.DocumentVersionID, ev.ChunkID, ev.ExternalWorkID = quote, url, docID, dvID, chunkID, externalID
		ev.Locator = map[string]any{}
		_ = json.Unmarshal(locatorRaw, &ev.Locator)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (e *Engine) GetEvidence(ctx context.Context, workspaceID, evidenceID string) (EvidenceView, error) {
	var ev EvidenceView
	var quote, url, docID, dvID, chunkID *string
	var locatorRaw []byte
	var externalID *string
	err := e.Store.Pool().QueryRow(ctx, `
		SELECT id,brief_id,index_epoch_id,evidence_type,title,quote,url,document_id,document_version_id,
			chunk_id,external_work_id,locator_json,score,retrieved_at
		FROM evidence WHERE workspace_id=$1 AND id=$2
	`, workspaceID, evidenceID).Scan(&ev.ID, &ev.BriefID, &ev.IndexEpochID, &ev.EvidenceType, &ev.Title, &quote, &url, &docID, &dvID, &chunkID, &externalID, &locatorRaw, &ev.Score, &ev.RetrievedAt)
	if err != nil {
		return EvidenceView{}, err
	}
	ev.Quote, ev.URL, ev.DocumentID, ev.DocumentVersionID, ev.ChunkID, ev.ExternalWorkID = quote, url, docID, dvID, chunkID, externalID
	ev.Locator = map[string]any{}
	_ = json.Unmarshal(locatorRaw, &ev.Locator)
	return ev, nil
}

func (e *Engine) ListEventsAfter(ctx context.Context, workspaceID, sessionID string, after int64, limit int) ([]domain.QueryEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := e.Store.Pool().Query(ctx, `
		SELECT id, session_id, seq, type, data_json, created_at
		FROM query_events
		WHERE workspace_id=$1 AND session_id=$2 AND seq>$3
		ORDER BY seq LIMIT $4
	`, workspaceID, sessionID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.QueryEvent
	for rows.Next() {
		var ev domain.QueryEvent
		var raw []byte
		if err := rows.Scan(&ev.ID, &ev.SessionID, &ev.Seq, &ev.Type, &raw, &ev.At); err != nil {
			return nil, err
		}
		ev.Data = map[string]any{}
		_ = json.Unmarshal(raw, &ev.Data)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func claimViewsToDomain(in []ClaimView) []domain.Claim {
	out := make([]domain.Claim, 0, len(in))
	for _, c := range in {
		x := domain.Claim{ID: c.ClaimID, ClaimType: domain.ClaimType(c.ClaimType), Text: c.Text,
			SupportLevel: c.SupportLevel, Confidence: c.Confidence, VerificationStatus: domain.VerificationStatus(c.VerificationStatus)}
		for _, r := range c.ClaimEvidence {
			span := ""
			if r.EvidenceSpan != nil {
				span = *r.EvidenceSpan
			}
			x.Evidence = append(x.Evidence, domain.ClaimEvidence{ClaimID: c.ClaimID, EvidenceID: r.EvidenceID,
				Stance: domain.ClaimStance(r.Stance), SupportScore: r.SupportScore, EvidenceSpan: span, Locator: r.Locator})
		}
		out = append(out, x)
	}
	return out
}

func findingViewsToDomain(in []FindingView) []domain.Claim {
	out := make([]domain.Claim, 0, len(in))
	for _, c := range in {
		x := domain.Claim{ID: c.ClaimID, ClaimType: domain.ClaimType(c.ClaimType), Text: c.Claim,
			SupportLevel: c.SupportLevel, Confidence: c.Confidence, VerificationStatus: domain.VerificationStatus(c.VerificationStatus)}
		for _, r := range c.ClaimEvidence {
			span := ""
			if r.EvidenceSpan != nil {
				span = *r.EvidenceSpan
			}
			x.Evidence = append(x.Evidence, domain.ClaimEvidence{ClaimID: c.ClaimID, EvidenceID: r.EvidenceID,
				Stance: domain.ClaimStance(r.Stance), SupportScore: r.SupportScore, EvidenceSpan: span, Locator: r.Locator})
		}
		out = append(out, x)
	}
	return out
}

func evidenceIDs(in []ClaimEvidenceView) []string {
	out := make([]string, 0, len(in))
	for _, r := range in {
		out = append(out, r.EvidenceID)
	}
	return out
}

func confidenceFor(rows []evidenceRow) float64 {
	if len(rows) == 0 {
		return 0
	}
	sum := 0.0
	for _, r := range rows {
		sum += clamp01(r.chunk.Score)
	}
	return sum / float64(len(rows))
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func stringPtr(s string) *string { return &s }

func epochIDString(s string) string { return s }
