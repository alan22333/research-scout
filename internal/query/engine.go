package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"research-scout/internal/domain"
	"research-scout/internal/embedding"
	"research-scout/internal/storage"
)

type EngineConfig struct {
	PlannerVersion         string
	RetrievalConfigVersion string
	PromptVersion          string
}

type Engine struct {
	Store  *storage.Store
	Config EngineConfig
	Stub   embedding.Stub
}

type SourceStatus struct {
	Source         string  `json:"source"`
	Status         string  `json:"status"`
	DegradedReason *string `json:"degraded_reason,omitempty"`
}

type SessionView struct {
	storage.Session
	CurrentTurnID string         `json:"current_turn_id,omitempty"`
	Mode          string         `json:"mode"`
	EventsURL     string         `json:"events_url"`
	SourceStatus  []SourceStatus `json:"source_status"`
}

type RunResult struct {
	Session SessionView  `json:"session"`
	Turn    storage.Turn `json:"turn"`
	Brief   BriefView    `json:"brief"`
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
	Locator           map[string]any `json:"locator"`
	Score             float64        `json:"score"`
	RetrievedAt       time.Time      `json:"retrieved_at"`
}

type GapView struct {
	ID                string  `json:"id"`
	GapType           string  `json:"gap_type"`
	MissingRelativeTo string  `json:"missing_relative_to"`
	Explanation       string  `json:"explanation"`
	Score             float64 `json:"score"`
}

type evidenceRow struct {
	view  EvidenceView
	chunk retrievedChunk
}

type retrievedChunk struct {
	ChunkID           string
	DocumentID        string
	DocumentVersionID string
	Title             string
	Content           string
	Section           string
	PageStart         *int
	PageEnd           *int
	CharStart         int
	CharEnd           int
	EvidenceType      domain.EvidenceType
	Score             float64
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
	turn := storage.Turn{}
	_, err = e.Store.Pool().Exec(ctx, `
		INSERT INTO query_turns
			(id,session_id,workspace_id,turn_no,question,mode,scope_snapshot_json,index_epoch_id,
			 planner_version,retrieval_config_version,prompt_version,status,phase)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11,'running','planning')
	`, turnID, sess.ID, workspaceID, turnNo, question, string(mode), scopeSnapshot, sess.IndexEpochID,
		e.Config.PlannerVersion, e.Config.RetrievalConfigVersion, e.Config.PromptVersion)
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
	if err := e.event(ctx, workspaceID, sess.ID, turnID, "phase_changed", map[string]any{"phase": domain.PhaseRetrievingLibrary, "source": "zotero"}); err != nil {
		return RunResult{}, err
	}

	chunks, err := e.retrieve(ctx, workspaceID, sess.IndexEpochID, project, question)
	if err != nil {
		_ = e.failTurn(ctx, workspaceID, turnID, err)
		return RunResult{}, err
	}
	for _, c := range chunks {
		if err := e.event(ctx, workspaceID, sess.ID, turnID, "evidence_ready", map[string]any{
			"chunk_id": c.ChunkID, "document_id": c.DocumentID, "evidence_type": c.EvidenceType,
		}); err != nil {
			return RunResult{}, err
		}
	}
	if err := e.event(ctx, workspaceID, sess.ID, turnID, "phase_changed", map[string]any{"phase": domain.PhaseRanking}); err != nil {
		return RunResult{}, err
	}
	if err := e.event(ctx, workspaceID, sess.ID, turnID, "phase_changed", map[string]any{"phase": domain.PhaseDrafting}); err != nil {
		return RunResult{}, err
	}

	brief, err := e.persistBrief(ctx, workspaceID, sess, turn, question, chunks)
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
		SourceStatus: []SourceStatus{{Source: "zotero", Status: "available"}, {Source: "semantic_scholar", Status: "skipped"}, {Source: "qwen_web_search", Status: "skipped"}},
	}
	return RunResult{Session: view, Turn: turn, Brief: brief}, nil
}

func nextTurnNo(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, workspaceID, sessionID string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT COALESCE(max(turn_no),0)+1 FROM query_turns WHERE workspace_id=$1 AND session_id=$2`, workspaceID, sessionID).Scan(&n)
	return n, err
}

func (e *Engine) retrieve(ctx context.Context, workspaceID, epochID string, project storage.Project, question string) ([]retrievedChunk, error) {
	scopeJSON, err := storage.JSON(project.CollectionIDs)
	if err != nil {
		return nil, err
	}
	rows, err := e.Store.Pool().Query(ctx, `
		SELECT c.id, c.document_id, c.document_version_id, d.title, c.content, c.section,
			c.page_start, c.page_end, c.char_start, c.char_end, d.evidence_type,
			(ts_rank_cd(c.search_tsv, plainto_tsquery('english',$3)) +
			 similarity(c.content,$3) * 0.25) AS score
		FROM chunks c
		JOIN index_epoch_documents ied
			ON ied.epoch_id=c.index_epoch_id
			AND ied.document_id=c.document_id
			AND ied.document_version_id=c.document_version_id
		JOIN documents d ON d.id=c.document_id AND d.workspace_id=c.workspace_id
		WHERE c.workspace_id=$1
			AND c.index_epoch_id=$2
			AND d.removed_at IS NULL
			AND d.index_status='indexed'
			AND d.evidence_type IN ('personal_fulltext','structured_abstract')
			AND (
				$4='all_library'
				OR EXISTS (
					SELECT 1 FROM document_collections dc
					WHERE dc.workspace_id=c.workspace_id
						AND dc.document_id=c.document_id
						AND dc.collection_id IN (
							SELECT jsonb_array_elements_text($5::jsonb)
						)
				)
			)
		ORDER BY score DESC, c.document_id, c.ordinal
		LIMIT 12
	`, workspaceID, epochID, question, project.ScopeMode, scopeJSON)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []retrievedChunk
	for rows.Next() {
		var c retrievedChunk
		var evidenceType string
		if err := rows.Scan(&c.ChunkID, &c.DocumentID, &c.DocumentVersionID, &c.Title, &c.Content, &c.Section, &c.PageStart, &c.PageEnd, &c.CharStart, &c.CharEnd, &evidenceType, &c.Score); err != nil {
			return nil, err
		}
		c.EvidenceType = domain.EvidenceType(evidenceType)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *Engine) persistBrief(ctx context.Context, workspaceID string, sess storage.Session, turn storage.Turn, question string, chunks []retrievedChunk) (BriefView, error) {
	tx, err := e.Store.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return BriefView{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	briefID := storage.StableID("brief", turn.ID)
	now := time.Now().UTC()
	sourceStatus := []SourceStatus{
		{Source: "zotero", Status: "available"},
		{Source: "semantic_scholar", Status: "skipped"},
		{Source: "qwen_web_search", Status: "skipped"},
	}
	sourceStatusJSON, err := storage.JSON(sourceStatus)
	if err != nil {
		return BriefView{}, err
	}

	evidenceViews := make([]EvidenceView, 0, len(chunks))
	evidenceRows := make([]evidenceRow, 0, len(chunks))
	for idx, c := range chunks {
		evidenceID := storage.StableID("evidence", briefID, c.ChunkID, fmt.Sprint(idx))
		locator := map[string]any{
			"index_epoch_id": epochIDString(turn.IndexEpochID), "chunk_id": c.ChunkID,
			"document_version_id": c.DocumentVersionID, "section": c.Section,
			"char_start": c.CharStart, "char_end": c.CharEnd,
		}
		if c.PageStart != nil {
			locator["page_start"] = *c.PageStart
		}
		if c.PageEnd != nil {
			locator["page_end"] = *c.PageEnd
		}
		quote := truncate(c.Content, 360)
		view := EvidenceView{
			ID: evidenceID, BriefID: briefID, IndexEpochID: turn.IndexEpochID, EvidenceType: string(c.EvidenceType),
			Title: c.Title, Quote: stringPtr(quote), DocumentID: stringPtr(c.DocumentID),
			DocumentVersionID: stringPtr(c.DocumentVersionID), ChunkID: stringPtr(c.ChunkID), Locator: locator,
			Score: c.Score, RetrievedAt: now,
		}
		evidenceViews = append(evidenceViews, view)
		evidenceRows = append(evidenceRows, evidenceRow{view: view, chunk: c})
	}

	summary := ""
	var summaryClaims []ClaimView
	var findings []FindingView
	var openQuestions []string
	if len(evidenceRows) == 0 {
		summary = "The current library index does not contain sufficient evidence to answer the question."
		openQuestions = append(openQuestions, "Import or sync documents with full text or abstracts that cover this question.")
	} else {
		summary = fmt.Sprintf("The library index contains %d relevant passage(s) for: %s", len(evidenceRows), question)
		summaryClaimEvidence := make([]ClaimEvidenceView, 0, len(evidenceRows))
		for _, row := range evidenceRows {
			span := truncate(row.chunk.Content, 220)
			summaryClaimEvidence = append(summaryClaimEvidence, ClaimEvidenceView{
				EvidenceID: row.view.ID, Stance: string(domain.ClaimSupports), SupportScore: clamp01(row.chunk.Score),
				EvidenceSpan: stringPtr(span), Locator: row.view.Locator,
			})
		}
		summaryClaims = append(summaryClaims, ClaimView{
			ClaimID: storage.StableID("claim", briefID, "summary", "1"), ClaimType: string(domain.ClaimSummary),
			Text: summary, SupportLevel: "sufficient", Confidence: confidenceFor(evidenceRows), VerificationStatus: string(domain.VerificationVerified),
			ClaimEvidence: summaryClaimEvidence,
		})
		for idx, row := range evidenceRows {
			excerpt := truncate(strings.TrimSpace(row.chunk.Content), 260)
			text := fmt.Sprintf("Relevant evidence from %s: %s", row.chunk.Title, excerpt)
			claimID := storage.StableID("claim", briefID, "finding", fmt.Sprint(idx+1))
			rel := ClaimEvidenceView{
				EvidenceID: row.view.ID, Stance: string(domain.ClaimSupports), SupportScore: clamp01(row.chunk.Score),
				EvidenceSpan: stringPtr(excerpt), Locator: row.view.Locator,
			}
			findings = append(findings, FindingView{
				ClaimID: claimID, ClaimType: string(domain.ClaimFinding), Claim: text, SupportLevel: "sufficient",
				Confidence: confidenceFor(evidenceRows), VerificationStatus: string(domain.VerificationVerified),
				EvidenceIDs: []string{row.view.ID}, ClaimEvidence: []ClaimEvidenceView{rel},
			})
		}
	}

	findingsJSON, _ := storage.JSON(findings)
	openJSON, _ := storage.JSON(openQuestions)
	if _, err := tx.Exec(ctx, `
		INSERT INTO briefs
			(id,session_id,turn_id,workspace_id,schema_version,summary,findings_json,method_timeline_json,
			 chronology_json,open_questions_json,source_status_json,index_epoch_id,retrieval_config_version,prompt_version,created_at)
		VALUES ($1,$2,$3,$4,'1.1',$5,$6::jsonb,'[]'::jsonb,'[]'::jsonb,$7::jsonb,$8::jsonb,$9,$10,$11,$12)
		ON CONFLICT (turn_id) DO NOTHING
	`, briefID, sess.ID, turn.ID, workspaceID, summary, findingsJSON, openJSON, sourceStatusJSON,
		turn.IndexEpochID, turn.RetrievalConfigVersion, turn.PromptVersion, now); err != nil {
		return BriefView{}, err
	}
	for _, row := range evidenceRows {
		locatorJSON, _ := storage.JSON(row.view.Locator)
		quote := ""
		if row.view.Quote != nil {
			quote = *row.view.Quote
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO evidence
				(id,brief_id,workspace_id,index_epoch_id,evidence_type,document_id,document_version_id,chunk_id,
				 title,quote,locator_json,score,retrieved_at,source_meta_json)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12,$13,'{"provider":"zotero_web_api_fixture"}'::jsonb)
			ON CONFLICT (id) DO NOTHING
		`, row.view.ID, briefID, workspaceID, turn.IndexEpochID, row.view.EvidenceType, row.view.DocumentID, row.view.DocumentVersionID,
			row.view.ChunkID, row.view.Title, quote, locatorJSON, row.view.Score, now); err != nil {
			return BriefView{}, err
		}
	}
	if err := e.persistClaims(ctx, tx, workspaceID, briefID, summaryClaims, findings); err != nil {
		return BriefView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BriefView{}, err
	}

	return BriefView{
		ID: briefID, SessionID: sess.ID, TurnID: turn.ID, SchemaVersion: "1.1", IndexEpochID: turn.IndexEpochID,
		RetrievalConfigVersion: turn.RetrievalConfigVersion, PromptVersion: turn.PromptVersion,
		Summary: summary, SummaryClaims: summaryClaims, Findings: findings,
		MethodTimeline: []map[string]any{}, Chronology: []map[string]any{}, Gaps: []GapView{},
		OpenQuestions: openQuestions, Citations: evidenceViews, SourceStatus: sourceStatus, CreatedAt: now,
	}, nil
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

func (e *Engine) event(ctx context.Context, workspaceID, sessionID, turnID, eventType string, data map[string]any) error {
	var seq int64
	if err := e.Store.Pool().QueryRow(ctx, `SELECT COALESCE(max(seq),0)+1 FROM query_events WHERE session_id=$1`, sessionID).Scan(&seq); err != nil {
		return err
	}
	dataJSON, err := storage.JSON(data)
	if err != nil {
		return err
	}
	id := storage.StableID("event", sessionID, fmt.Sprint(seq), eventType)
	_, err = e.Store.Pool().Exec(ctx, `
		INSERT INTO query_events (id,session_id,turn_id,workspace_id,seq,type,data_json)
		VALUES ($1,$2,NULLIF($3,''),$4,$5,$6,$7::jsonb)
		ON CONFLICT (session_id, seq) DO NOTHING
	`, id, sessionID, turnID, workspaceID, seq, eventType, dataJSON)
	return err
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
	out.Gaps = []GapView{}
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

func (e *Engine) ListEvidence(ctx context.Context, workspaceID, briefID string) ([]EvidenceView, error) {
	rows, err := e.Store.Pool().Query(ctx, `
		SELECT id,brief_id,index_epoch_id,evidence_type,title,quote,url,document_id,document_version_id,
			chunk_id,locator_json,score,retrieved_at
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
		var quote, url, docID, dvID, chunkID *string
		var locatorRaw []byte
		if err := rows.Scan(&ev.ID, &ev.BriefID, &ev.IndexEpochID, &ev.EvidenceType, &ev.Title, &quote, &url, &docID, &dvID, &chunkID, &locatorRaw, &ev.Score, &ev.RetrievedAt); err != nil {
			return nil, err
		}
		ev.Quote, ev.URL, ev.DocumentID, ev.DocumentVersionID, ev.ChunkID = quote, url, docID, dvID, chunkID
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
	err := e.Store.Pool().QueryRow(ctx, `
		SELECT id,brief_id,index_epoch_id,evidence_type,title,quote,url,document_id,document_version_id,
			chunk_id,locator_json,score,retrieved_at
		FROM evidence WHERE workspace_id=$1 AND id=$2
	`, workspaceID, evidenceID).Scan(&ev.ID, &ev.BriefID, &ev.IndexEpochID, &ev.EvidenceType, &ev.Title, &quote, &url, &docID, &dvID, &chunkID, &locatorRaw, &ev.Score, &ev.RetrievedAt)
	if err != nil {
		return EvidenceView{}, err
	}
	ev.Quote, ev.URL, ev.DocumentID, ev.DocumentVersionID, ev.ChunkID = quote, url, docID, dvID, chunkID
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
