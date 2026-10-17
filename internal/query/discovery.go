package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"research-scout/internal/discovery"
	"research-scout/internal/domain"
	"research-scout/internal/planner"
	"research-scout/internal/retrieval"
	"research-scout/internal/storage"
)

// DiscoveryRunner is the query-side port for external discovery. Tests inject a
// stub; production wires discovery.Service.
type DiscoveryRunner interface {
	Discover(ctx context.Context, req discovery.ServiceRequest) (discovery.ServiceResult, error)
}

// modeAllowsStructured reports whether a retrieval mode may call S2/OpenAlex.
// The frozen matrix lives in the discovery package so it has a single owner.
func modeAllowsStructured(mode domain.RetrievalMode) bool {
	return discovery.ModeAllowsStructured(mode)
}

// modeAllowsWebSearch reports whether a retrieval mode may call provider-native
// web search. Only landscape may, and only after structured coverage leaves gaps.
func modeAllowsWebSearch(mode domain.RetrievalMode) bool {
	return discovery.ModeAllowsWebSearch(mode)
}

const (
	externalEvidenceScore = 0.5
	webContextScore       = 0.35

	maxEvidenceChunks      = 12
	perDocumentEvidenceCap = 2
)

// emptyDiscoveryResult is the deterministic "no external work" result used by
// my_library turns and as a safe default when discovery is unconfigured.
func emptyDiscoveryResult() discovery.ServiceResult {
	return discovery.ServiceResult{
		Works:        []discovery.ExternalWork{},
		Gaps:         []discovery.Gap{},
		WebContexts:  []discovery.WebContext{},
		SourceStatus: []discovery.SourceStatusEntry{},
	}
}

// runDiscovery performs external discovery for one turn. Provider failures are
// degraded into visible source statuses instead of failing the turn.
func (e *Engine) runDiscovery(ctx context.Context, workspaceID string, mode domain.RetrievalMode, project storage.Project, question, webTopic string, chunks []retrievedChunk) (discovery.ServiceResult, []retrievedChunk) {
	if e.Discovery == nil {
		empty := emptyDiscoveryResult()
		empty.SourceStatus = []discovery.SourceStatusEntry{
			{Source: "semantic_scholar", Status: domain.SourceSkipped, Reason: "discovery_not_configured"},
			{Source: "openalex", Status: domain.SourceSkipped, Reason: "discovery_not_configured"},
			{Source: "qwen_web_search", Status: domain.SourceSkipped, Reason: "discovery_not_configured"},
		}
		return empty, nil
	}
	projection, err := e.Store.LibraryProjectionForScope(ctx, workspaceID, project.ScopeMode, project.CollectionIDs)
	if err != nil {
		empty := emptyDiscoveryResult()
		empty.SourceStatus = discoveryFailureStatuses("library_projection_failed")
		return empty, nil
	}
	library := make([]discovery.LibraryDoc, 0, len(projection))
	byDoc := map[string]storage.LibraryProjection{}
	for _, row := range projection {
		library = append(library, discovery.LibraryDoc{
			DocumentID: row.DocumentID, Title: row.Title, Year: row.Year, DOI: row.DOI,
			InScope: row.InScope, Missing: row.Missing,
		})
		byDoc[row.DocumentID] = row
	}
	seeds := make([]discovery.LibraryDoc, 0, len(chunks))
	seen := map[string]bool{}
	for _, c := range chunks {
		if c.DocumentID == "" || seen[c.DocumentID] {
			continue
		}
		row, ok := byDoc[c.DocumentID]
		if !ok {
			continue
		}
		seen[c.DocumentID] = true
		seeds = append(seeds, discovery.LibraryDoc{
			DocumentID: row.DocumentID, Title: row.Title, Year: row.Year, DOI: row.DOI, InScope: row.InScope,
		})
	}

	result, err := e.Discovery.Discover(ctx, discovery.ServiceRequest{
		WorkspaceID:       workspaceID,
		Topic:             strings.TrimSpace(question),
		Mode:              mode,
		Library:           library,
		Seeds:             seeds,
		DeidentifiedTopic: strings.TrimSpace(webTopic),
		LibraryUnknown:    len(library) == 0,
	})
	if err != nil {
		empty := emptyDiscoveryResult()
		empty.SourceStatus = discoveryFailureStatuses("discovery_internal_error")
		return empty, nil
	}
	if result.Works == nil {
		result.Works = []discovery.ExternalWork{}
	}
	if result.Gaps == nil {
		result.Gaps = []discovery.Gap{}
	}
	if result.WebContexts == nil {
		result.WebContexts = []discovery.WebContext{}
	}

	records := make([]storage.ExternalWorkRecord, 0, len(result.Works))
	idByIdentity := make(map[string]string, len(result.Works))
	for i := range result.Works {
		rec := ExternalWorkToRecord(workspaceID, result.Works[i])
		records = append(records, rec)
		result.Works[i].ID = rec.ID
		if result.Works[i].IdentityKey != "" {
			idByIdentity[result.Works[i].IdentityKey] = rec.ID
		}
	}
	// Gaps carry a copy of the work they reference, produced before the engine
	// assigned storage ids. Point every gap at the same external_works row id so
	// gap.external_work_id, evidence.external_work_id and external_works.id all
	// agree and the work stays fetchable through /discovery/works/{id}.
	for i := range result.Gaps {
		if id, ok := idByIdentity[result.Gaps[i].Work.IdentityKey]; ok {
			result.Gaps[i].Work.ID = id
		}
	}
	if err := e.Store.UpsertExternalWorks(ctx, workspaceID, records); err != nil {
		empty := emptyDiscoveryResult()
		empty.SourceStatus = discoveryFailureStatuses("external_work_persist_failed")
		return empty, nil
	}

	return result, evidenceChunksFromDiscovery(result)
}

func discoveryFailureStatuses(reason string) []discovery.SourceStatusEntry {
	return []discovery.SourceStatusEntry{
		{Source: "semantic_scholar", Status: domain.SourceDegraded, Reason: reason},
		{Source: "openalex", Status: domain.SourceDegraded, Reason: reason},
		{Source: "qwen_web_search", Status: domain.SourceSkipped, Reason: reason},
	}
}

// ExternalWorkToRecord maps a normalized external work onto its row shape. The
// row id is a stable function of the workspace and the identity key.
func ExternalWorkToRecord(workspaceID string, w discovery.ExternalWork) storage.ExternalWorkRecord {
	sources := make([]string, 0, len(w.Sources))
	for _, s := range w.Sources {
		sources = append(sources, string(s))
	}
	if len(sources) == 0 && w.Source != "" {
		sources = append(sources, string(w.Source))
	}
	key := w.IdentityKey
	if key == "" {
		key = discovery.Key(w)
	}
	status := string(w.Status)
	if status == "" {
		status = string(discovery.ExternalStatusAvailable)
	}
	return storage.ExternalWorkRecord{
		ID:            storage.ExternalWorkID(workspaceID, key),
		WorkspaceID:   workspaceID,
		IdentityKey:   key,
		DOI:           discovery.NormalizeDOI(w.DOI),
		S2ID:          w.S2ID,
		OpenAlexID:    w.OpenAlexID,
		PMID:          w.PMID,
		Title:         w.Title,
		Abstract:      w.Abstract,
		Year:          w.Year,
		Venue:         w.Venue,
		CitationCount: w.CitationCount,
		Type:          w.Type,
		Status:        status,
		Sources:       sources,
		RetrievedAt:   w.RetrievedAt,
	}
}

// ExternalWorkFromRecord maps a stored row back to the normalized shape.
func ExternalWorkFromRecord(rec storage.ExternalWorkRecord) discovery.ExternalWork {
	sources := make([]discovery.DiscoverySource, 0, len(rec.Sources))
	for _, s := range rec.Sources {
		sources = append(sources, discovery.DiscoverySource(s))
	}
	var source discovery.DiscoverySource
	if len(sources) > 0 {
		source = sources[0]
	}
	return discovery.ExternalWork{
		ID: rec.ID, IdentityKey: rec.IdentityKey, DOI: rec.DOI, S2ID: rec.S2ID, OpenAlexID: rec.OpenAlexID,
		PMID: rec.PMID, Title: rec.Title, Abstract: rec.Abstract, Year: rec.Year, Venue: rec.Venue,
		CitationCount: rec.CitationCount, Type: rec.Type, Source: source, Sources: sources,
		Status: discovery.ExternalStatus(rec.Status), RetrievedAt: rec.RetrievedAt,
	}
}

// evidenceChunksFromDiscovery converts discovered works and web context into
// retrieval candidates. External abstracts are never fulltext; web context is
// only ever a background clue.
func evidenceChunksFromDiscovery(result discovery.ServiceResult) []retrievedChunk {
	out := make([]retrievedChunk, 0, len(result.Works)+len(result.WebContexts))
	for _, w := range result.Works {
		abstract := strings.TrimSpace(w.Abstract)
		if abstract == "" || w.ID == "" {
			continue
		}
		if w.AlreadyInLibrary && w.InCurrentScope {
			// Already-covered works are not evidence again.
			continue
		}
		out = append(out, retrievedChunk{
			ChunkID: "external:" + w.ID, ExternalWorkID: w.ID, Title: w.Title, Content: abstract,
			EvidenceType: domain.EvidenceStructuredAbstract, Score: externalEvidenceScore,
			Provider: string(w.Source), RetrievedAt: w.RetrievedAt,
		})
	}
	for _, ctxItem := range result.WebContexts {
		url := strings.TrimSpace(ctxItem.URL)
		if url == "" {
			continue
		}
		out = append(out, retrievedChunk{
			ChunkID: "web:" + shortHash(url), URL: url, Title: ctxItem.Title, Content: ctxItem.Snippet,
			EvidenceType: domain.EvidenceWebContext, Score: webContextScore,
			Provider: ctxItem.Provider, Model: ctxItem.Model, RetrievedAt: ctxItem.RetrievedAt,
		})
	}
	return out
}

// mergeEvidence appends discovery evidence to the library evidence, dedupes by
// chunk identity, orders by the frozen evidence priority (personal fulltext
// first), and applies shared chunk/document caps so a broad external result set
// can never push out personal-library evidence.
func mergeEvidence(library, external []retrievedChunk, maxDocuments int) []retrievedChunk {
	if len(external) == 0 {
		return library
	}
	if maxDocuments <= 0 {
		maxDocuments = 6
	}
	combined := make([]retrievedChunk, 0, len(library)+len(external))
	seen := map[string]bool{}
	for _, c := range append(append([]retrievedChunk{}, library...), external...) {
		if c.ChunkID != "" {
			if seen[c.ChunkID] {
				continue
			}
			seen[c.ChunkID] = true
		}
		combined = append(combined, c)
	}
	sort.SliceStable(combined, func(i, j int) bool {
		pi, pj := evidencePriority(combined[i].EvidenceType), evidencePriority(combined[j].EvidenceType)
		if pi != pj {
			return pi < pj
		}
		if combined[i].Score != combined[j].Score {
			return combined[i].Score > combined[j].Score
		}
		return combined[i].ChunkID < combined[j].ChunkID
	})
	out := make([]retrievedChunk, 0, maxEvidenceChunks)
	docCounts := map[string]int{}
	for _, c := range combined {
		if len(out) >= maxEvidenceChunks {
			break
		}
		docKey := evidenceDocumentKey(c)
		if docKey != "" {
			if docCounts[docKey] >= perDocumentEvidenceCap {
				continue
			}
			if docCounts[docKey] == 0 && len(docCounts) >= maxDocuments {
				continue
			}
			docCounts[docKey]++
		}
		out = append(out, c)
	}
	return out
}

func evidencePriority(t domain.EvidenceType) int {
	switch t {
	case domain.EvidencePersonalFulltext:
		return 0
	case domain.EvidenceStructuredAbstract:
		return 1
	default:
		return 2
	}
}

func evidenceDocumentKey(c retrievedChunk) string {
	if c.DocumentID != "" {
		return "doc:" + c.DocumentID
	}
	if c.ExternalWorkID != "" {
		return "external:" + c.ExternalWorkID
	}
	if c.URL != "" {
		return "web:" + c.URL
	}
	return ""
}

func maxDocumentsForMode(mode domain.RetrievalMode) int {
	switch mode {
	case domain.RetrievalModeGapFill, domain.RetrievalModeLandscape:
		return 8
	default:
		return 6
	}
}

// deidentifiedTopic reduces a planned query to a short topical phrase that is
// safe to send to provider-native web search: emails, URLs, DOIs, file paths
// and quoted strings are stripped, whitespace is collapsed and the result is
// bounded. It never includes personal fulltext or library metadata.
func deidentifiedTopic(plan planner.QueryPlan) string {
	text := strings.TrimSpace(plan.Question)
	if len(plan.Subqueries) > 0 {
		if primary := strings.TrimSpace(plan.Subqueries[0].Query); primary != "" {
			text = primary
		}
	}
	text = deidentifyEmail.ReplaceAllString(text, " ")
	text = deidentifyURL.ReplaceAllString(text, " ")
	text = deidentifyDOI.ReplaceAllString(text, " ")
	text = deidentifyPath.ReplaceAllString(text, " ")
	text = deidentifyQuoted.ReplaceAllString(text, " ")
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 200 {
		text = strings.TrimSpace(text[:200])
	}
	return text
}

var (
	deidentifyEmail  = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)
	deidentifyURL    = regexp.MustCompile(`(?i)\bhttps?://\S+|\bwww\.\S+`)
	deidentifyDOI    = regexp.MustCompile(`(?i)\b10\.\d{4,9}/\S+`)
	deidentifyPath   = regexp.MustCompile(`(?:/[\w.\-]+){2,}|[A-Za-z]:\\\S+`)
	deidentifyQuoted = regexp.MustCompile(`["'“”‘’][^"'“”‘’]{1,200}["'“”‘’]`)
)

// shortHash returns a short, stable identifier for URLs or other opaque
// provider values that are used only in internal chunk identities.
func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

// sourceStatusesForTurn records the actual Stage 3 sources of one turn: the
// zotero library, whichever structured discovery sources ran, the web search
// gate and the rerank path. Missing provider reports are made explicit rather
// than silently omitted.
func sourceStatusesForTurn(rerankOutcome retrieval.RerankOutcome, discoveryStatuses []discovery.SourceStatusEntry) []SourceStatus {
	statuses := []SourceStatus{{Source: "zotero", Status: string(domain.SourceAvailable)}}
	seen := map[string]bool{}
	for _, entry := range discoveryStatuses {
		source := strings.TrimSpace(entry.Source)
		if source == "" {
			continue
		}
		status := SourceStatus{Source: source, Status: string(entry.Status)}
		if strings.TrimSpace(entry.Reason) != "" {
			reason := strings.TrimSpace(entry.Reason)
			status.DegradedReason = &reason
		}
		statuses = append(statuses, status)
		seen[source] = true
	}
	for _, source := range []string{"semantic_scholar", "openalex", "qwen_web_search"} {
		if !seen[source] {
			reason := "not_reported"
			statuses = append(statuses, SourceStatus{Source: source, Status: string(domain.SourceSkipped), DegradedReason: &reason})
		}
	}
	rerankStatuses := sourceStatusesForRerank(rerankOutcome)
	for _, status := range rerankStatuses {
		if status.Source == "rerank" {
			statuses = append(statuses, status)
			break
		}
	}
	return statuses
}

// ExternalSearchPage is the read model for /discovery/search. Works are
// normalized external records; SourceStatus reports per-source coverage so the
// caller can render degraded results instead of silently claiming completeness.
type ExternalSearchPage struct {
	Items        []ExternalWorkView `json:"items"`
	SourceStatus []SourceStatus     `json:"source_status"`
	NextCursor   string             `json:"next_cursor,omitempty"`
}

// SearchExternal runs an on-demand structured scholarly search for the
// /discovery/search route. It never calls web search: the route only exposes
// S2/OpenAlex discovery and is mode-gated to gap_fill/landscape so my_library
// never triggers an external call.
func (e *Engine) SearchExternal(ctx context.Context, workspaceID, topic string, mode domain.RetrievalMode, sources []string) (ExternalSearchPage, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return ExternalSearchPage{}, fmt.Errorf("empty discovery query")
	}
	if !modeAllowsStructured(mode) {
		return ExternalSearchPage{}, fmt.Errorf("mode %q does not allow external discovery", mode)
	}
	if e.Discovery == nil {
		return ExternalSearchPage{
			Items:        []ExternalWorkView{},
			SourceStatus: discoverySourceStatuses(nil),
		}, nil
	}
	projection, err := e.Store.LibraryProjectionForScope(ctx, workspaceID, "all_library", nil)
	if err != nil {
		return ExternalSearchPage{Items: []ExternalWorkView{}, SourceStatus: discoverySourceStatuses(discoveryFailureStatuses("library_projection_failed"))}, nil
	}
	library := make([]discovery.LibraryDoc, 0, len(projection))
	for _, row := range projection {
		library = append(library, discovery.LibraryDoc{
			DocumentID: row.DocumentID, Title: row.Title, Year: row.Year, DOI: row.DOI,
			InScope: row.InScope, Missing: row.Missing,
		})
	}
	result, err := e.Discovery.Discover(ctx, discovery.ServiceRequest{
		WorkspaceID:    workspaceID,
		Topic:          topic,
		Mode:           domain.RetrievalModeGapFill,
		Library:        library,
		LibraryUnknown: len(library) == 0,
	})
	if err != nil {
		return ExternalSearchPage{Items: []ExternalWorkView{}, SourceStatus: discoverySourceStatuses(discoveryFailureStatuses("discovery_internal_error"))}, nil
	}
	records := make([]storage.ExternalWorkRecord, 0, len(result.Works))
	for i := range result.Works {
		rec := ExternalWorkToRecord(workspaceID, result.Works[i])
		records = append(records, rec)
		result.Works[i].ID = rec.ID
	}
	if err := e.Store.UpsertExternalWorks(ctx, workspaceID, records); err != nil {
		return ExternalSearchPage{Items: []ExternalWorkView{}, SourceStatus: discoverySourceStatuses(discoveryFailureStatuses("external_work_persist_failed"))}, nil
	}
	return ExternalSearchPage{
		Items:        externalWorkViews(result.Works, sources),
		SourceStatus: discoverySourceStatuses(result.SourceStatus),
	}, nil
}

// GetExternalWorkView reads one workspace-scoped external work for the detail
// route. Cross-workspace ids resolve to storage.ErrNotFound.
func (e *Engine) GetExternalWorkView(ctx context.Context, workspaceID, id string) (ExternalWorkView, error) {
	rec, err := e.Store.GetExternalWork(ctx, workspaceID, id)
	if err != nil {
		return ExternalWorkView{}, err
	}
	return ExternalWorkViewFromRecord(rec), nil
}

// ListCitedWorks returns the read-only citation/reference relations stored for
// one external work. It never performs a second discovery hop.
func (e *Engine) ListCitedWorks(ctx context.Context, workspaceID, id string) ([]ExternalWorkView, error) {
	if _, err := e.Store.GetExternalWork(ctx, workspaceID, id); err != nil {
		return nil, err
	}
	recs, err := e.Store.ListCitedWorks(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}
	out := make([]ExternalWorkView, 0, len(recs))
	for _, rec := range recs {
		out = append(out, ExternalWorkViewFromRecord(rec))
	}
	return out, nil
}

// ExternalWorkView is the snake_case JSON shape published by the OpenAPI
// contract. It carries per-work provenance so the source of each result is
// visible without leaking secrets.
type ExternalWorkView struct {
	ID               string       `json:"id"`
	IdentityKey      string       `json:"identity_key"`
	DOI              *string      `json:"doi,omitempty"`
	S2ID             *string      `json:"s2_id,omitempty"`
	OpenAlexID       *string      `json:"openalex_id,omitempty"`
	PMID             *string      `json:"pmid,omitempty"`
	Title            string       `json:"title"`
	Abstract         *string      `json:"abstract,omitempty"`
	Year             *int         `json:"year,omitempty"`
	Venue            *string      `json:"venue,omitempty"`
	CitationCount    *int         `json:"citation_count,omitempty"`
	Type             *string      `json:"type,omitempty"`
	SourceStatus     SourceStatus `json:"source_status"`
	AlreadyInLibrary bool         `json:"already_in_library,omitempty"`
}

func externalWorkViews(works []discovery.ExternalWork, sources []string) []ExternalWorkView {
	allow := map[string]bool{}
	for _, s := range sources {
		if s = strings.TrimSpace(s); s != "" {
			allow[s] = true
		}
	}
	out := make([]ExternalWorkView, 0, len(works))
	for _, w := range works {
		if len(allow) > 0 && !workHasSource(w, allow) {
			continue
		}
		out = append(out, externalWorkView(w))
	}
	return out
}

func workHasSource(w discovery.ExternalWork, allow map[string]bool) bool {
	if allow[string(w.Source)] {
		return true
	}
	for _, s := range w.Sources {
		if allow[string(s)] {
			return true
		}
	}
	return false
}

func externalWorkView(w discovery.ExternalWork) ExternalWorkView {
	view := ExternalWorkView{
		ID: w.ID, IdentityKey: w.IdentityKey, Title: w.Title, AlreadyInLibrary: w.AlreadyInLibrary,
		SourceStatus: SourceStatus{Source: string(w.Source), Status: string(domain.SourceAvailable)},
	}
	if w.Status == discovery.ExternalStatusDegraded || w.Status == discovery.ExternalStatusInvalid {
		view.SourceStatus.Status = string(domain.SourceDegraded)
	}
	view.DOI = optionalString(w.DOI)
	view.S2ID = optionalString(w.S2ID)
	view.OpenAlexID = optionalString(w.OpenAlexID)
	view.PMID = optionalString(w.PMID)
	view.Abstract = optionalString(w.Abstract)
	view.Venue = optionalString(w.Venue)
	view.Type = optionalString(w.Type)
	if w.Year > 0 {
		year := w.Year
		view.Year = &year
	}
	if w.CitationCount > 0 {
		cites := w.CitationCount
		view.CitationCount = &cites
	}
	return view
}

// ExternalWorkViewFromRecord maps a persisted external work row onto the API
// view. Persisted rows always come from an available sync, so they report an
// available source status.
func ExternalWorkViewFromRecord(rec storage.ExternalWorkRecord) ExternalWorkView {
	view := ExternalWorkView{
		ID: rec.ID, IdentityKey: rec.IdentityKey, Title: rec.Title,
		AlreadyInLibrary: false,
	}
	source := ""
	if len(rec.Sources) > 0 {
		source = rec.Sources[0]
	}
	view.SourceStatus = SourceStatus{Source: source, Status: string(domain.SourceAvailable)}
	if rec.Status == string(discovery.ExternalStatusDegraded) || rec.Status == string(discovery.ExternalStatusInvalid) {
		view.SourceStatus.Status = string(domain.SourceDegraded)
	}
	view.DOI = optionalString(rec.DOI)
	view.S2ID = optionalString(rec.S2ID)
	view.OpenAlexID = optionalString(rec.OpenAlexID)
	view.PMID = optionalString(rec.PMID)
	view.Abstract = optionalString(rec.Abstract)
	view.Venue = optionalString(rec.Venue)
	view.Type = optionalString(rec.Type)
	if rec.Year > 0 {
		year := rec.Year
		view.Year = &year
	}
	if rec.CitationCount > 0 {
		cites := rec.CitationCount
		view.CitationCount = &cites
	}
	return view
}

func optionalString(v string) *string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return &v
}

// discoverySourceStatuses renders the external provider statuses for one
// discovery response, making unreported sources explicit instead of omitting
// them.
func discoverySourceStatuses(entries []discovery.SourceStatusEntry) []SourceStatus {
	statuses := make([]SourceStatus, 0, len(entries)+3)
	seen := map[string]bool{}
	for _, entry := range entries {
		source := strings.TrimSpace(entry.Source)
		if source == "" {
			continue
		}
		status := SourceStatus{Source: source, Status: string(entry.Status)}
		if strings.TrimSpace(entry.Reason) != "" {
			reason := strings.TrimSpace(entry.Reason)
			status.DegradedReason = &reason
		}
		statuses = append(statuses, status)
		seen[source] = true
	}
	for _, source := range []string{"semantic_scholar", "openalex", "qwen_web_search"} {
		if !seen[source] {
			reason := "not_reported"
			statuses = append(statuses, SourceStatus{Source: source, Status: string(domain.SourceSkipped), DegradedReason: &reason})
		}
	}
	return statuses
}
