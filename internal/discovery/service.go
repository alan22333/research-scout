package discovery

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"research-scout/internal/domain"
)

// ServiceConfig bounds one external-discovery run. Every field has a safe
// default so a zero value never triggers unbounded provider work.
type ServiceConfig struct {
	YearFrom           int
	YearTo             int
	Limit              int
	WebLimit           int
	NowYear            int
	CanonicalCitations int
	SourceTimeout      time.Duration
	TotalTimeout       time.Duration
	DiscoveryEnabled   bool
	WebSearchEnabled   bool
}

func (c ServiceConfig) withDefaults() ServiceConfig {
	if c.YearFrom == 0 {
		c.YearFrom = 2016
	}
	if c.YearTo == 0 {
		c.YearTo = time.Now().UTC().Year()
	}
	if c.Limit <= 0 {
		c.Limit = 20
	}
	if c.WebLimit <= 0 {
		c.WebLimit = 5
	}
	if c.NowYear == 0 {
		c.NowYear = c.YearTo
	}
	if c.SourceTimeout <= 0 {
		c.SourceTimeout = 8 * time.Second
	}
	if c.TotalTimeout <= 0 {
		c.TotalTimeout = 20 * time.Second
	}
	return c
}

// Service orchestrates structured discovery, library de-duplication, gap
// detection and optional provider-native web search for one turn.
type Service struct {
	Sources []ScholarlyDiscoverySource
	Web     WebSearchProvider
	Config  ServiceConfig
}

// ServiceRequest is the per-turn input. Library is the whole My Library
// projection; Seeds are the library documents that share the query topic.
type ServiceRequest struct {
	WorkspaceID string
	Topic       string
	Mode        domain.RetrievalMode
	Library     []LibraryDoc
	Seeds       []LibraryDoc
	// DeidentifiedTopic is the only text allowed to leave the process for web
	// search. Callers must strip personal metadata before setting it.
	DeidentifiedTopic string
	// LibraryUnknown marks a run without a synced My Library projection, so gaps
	// are reported as external_only instead of personal_library.
	LibraryUnknown bool
}

// SourceStatusEntry is one externally visible provider status.
type SourceStatusEntry struct {
	Source string
	Status domain.SourceStatus
	Reason string
}

// ServiceResult is the deterministic output of one discovery run.
type ServiceResult struct {
	Works            []ExternalWork
	Gaps             []Gap
	WebContexts      []WebContext
	SourceStatus     []SourceStatusEntry
	WebSkippedReason string
}

// ModeAllowsStructured reports whether a retrieval mode may call S2/OpenAlex.
func ModeAllowsStructured(mode domain.RetrievalMode) bool {
	return mode == domain.RetrievalModeGapFill || mode == domain.RetrievalModeLandscape
}

// ModeAllowsWebSearch reports whether a retrieval mode may call web search.
func ModeAllowsWebSearch(mode domain.RetrievalMode) bool {
	return mode == domain.RetrievalModeLandscape
}

// Discover runs the frozen mode/source matrix and never returns a silent
// success: every skipped or failed source is reported in SourceStatus.
func (s *Service) Discover(ctx context.Context, req ServiceRequest) (ServiceResult, error) {
	cfg := s.Config.withDefaults()
	res := ServiceResult{
		Works:        []ExternalWork{},
		Gaps:         []Gap{},
		WebContexts:  []WebContext{},
		SourceStatus: []SourceStatusEntry{},
	}

	if !ModeAllowsStructured(req.Mode) || !cfg.DiscoveryEnabled {
		reason := "mode_my_library"
		if ModeAllowsStructured(req.Mode) && !cfg.DiscoveryEnabled {
			reason = "discovery_disabled"
		}
		for _, src := range s.structuredSources() {
			res.SourceStatus = append(res.SourceStatus, SourceStatusEntry{
				Source: string(src.Name()), Status: domain.SourceSkipped, Reason: reason,
			})
		}
		res.SourceStatus = append(res.SourceStatus, webStatusEntry(
			WebSearchSkipped, "not_landscape_or_discovery_disabled"))
		res.WebSkippedReason = "not_landscape_or_discovery_disabled"
		return res, nil
	}

	topic := strings.TrimSpace(req.Topic)
	if topic == "" {
		for _, src := range s.structuredSources() {
			res.SourceStatus = append(res.SourceStatus, SourceStatusEntry{
				Source: string(src.Name()), Status: domain.SourceSkipped, Reason: "empty_query",
			})
		}
		res.SourceStatus = append(res.SourceStatus, webStatusEntry(WebSearchSkipped, "empty_query"))
		res.WebSkippedReason = "empty_query"
		return res, nil
	}

	runCtx, cancel := context.WithTimeout(ctx, cfg.TotalTimeout)
	defer cancel()

	type outcome struct {
		idx  int
		resp DiscoveryResponse
		err  error
	}
	sources := s.structuredSources()
	outcomes := make(chan outcome, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func(i int, src ScholarlyDiscoverySource) {
			defer wg.Done()
			resp, err := s.searchOne(runCtx, src, topic, req.Mode, cfg)
			outcomes <- outcome{idx: i, resp: resp, err: err}
		}(i, src)
	}
	wg.Wait()
	close(outcomes)

	collected := make([]outcome, 0, len(sources))
	for o := range outcomes {
		collected = append(collected, o)
	}
	sort.Slice(collected, func(i, j int) bool { return collected[i].idx < collected[j].idx })

	var works []ExternalWork
	for _, o := range collected {
		name := string(sources[o.idx].Name())
		switch {
		case o.err != nil:
			res.SourceStatus = append(res.SourceStatus, SourceStatusEntry{
				Source: name, Status: statusForError(o.err), Reason: errorReason(o.err),
			})
		default:
			status := domain.SourceAvailable
			if o.resp.Status == domain.SourceDegraded || o.resp.Status == domain.SourceUnavailable {
				status = o.resp.Status
			}
			res.SourceStatus = append(res.SourceStatus, SourceStatusEntry{
				Source: name, Status: status, Reason: strings.TrimSpace(o.resp.Reason),
			})
			works = append(works, o.resp.Works...)
		}
	}

	merged := MergeAll(works)
	merged = DedupAgainstLibrary(merged, req.Library)
	res.Works = merged
	res.Gaps = DetectGaps(GapInput{
		NowYear: cfg.NowYear, Works: merged, Seeds: req.Seeds, Library: req.Library,
		CanonicalCitations: cfg.CanonicalCitations, LibraryUnknown: req.LibraryUnknown,
	})
	if res.Gaps == nil {
		res.Gaps = []Gap{}
	}

	webEntry, webContexts := s.maybeWebSearch(runCtx, req, cfg, len(res.Gaps))
	res.SourceStatus = append(res.SourceStatus, webEntry)
	res.WebContexts = webContexts
	res.WebSkippedReason = webEntry.Reason
	return res, nil
}

func (s *Service) structuredSources() []ScholarlyDiscoverySource {
	out := make([]ScholarlyDiscoverySource, 0, len(s.Sources))
	for _, src := range s.Sources {
		if src != nil {
			out = append(out, src)
		}
	}
	return out
}

func (s *Service) searchOne(ctx context.Context, src ScholarlyDiscoverySource, topic string, mode domain.RetrievalMode, cfg ServiceConfig) (DiscoveryResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, cfg.SourceTimeout)
	defer cancel()
	return src.Search(callCtx, SearchQuery{
		Topic: topic, Mode: mode, YearFrom: cfg.YearFrom, YearTo: cfg.YearTo, Limit: cfg.Limit,
	})
}

// maybeWebSearch applies the frozen gate: web search only runs in landscape
// mode, only when it is enabled, and only when structured discovery still
// leaves gaps.
func (s *Service) maybeWebSearch(ctx context.Context, req ServiceRequest, cfg ServiceConfig, gapCount int) (SourceStatusEntry, []WebContext) {
	name := "qwen_web_search"
	if !ModeAllowsWebSearch(req.Mode) {
		return SourceStatusEntry{Source: name, Status: domain.SourceSkipped, Reason: "not_landscape"}, []WebContext{}
	}
	if !cfg.WebSearchEnabled {
		return SourceStatusEntry{Source: name, Status: domain.SourceSkipped, Reason: "web_search_disabled"}, []WebContext{}
	}
	if s.Web == nil {
		return SourceStatusEntry{Source: name, Status: domain.SourceUnavailable, Reason: "web_search_provider_missing"}, []WebContext{}
	}
	if gapCount == 0 {
		return SourceStatusEntry{Source: name, Status: domain.SourceSkipped, Reason: "structured_coverage_sufficient"}, []WebContext{}
	}
	topic := strings.TrimSpace(req.DeidentifiedTopic)
	if topic == "" {
		topic = strings.TrimSpace(req.Topic)
	}
	if topic == "" {
		return SourceStatusEntry{Source: name, Status: domain.SourceSkipped, Reason: "empty_query"}, []WebContext{}
	}
	callCtx, cancel := context.WithTimeout(ctx, cfg.SourceTimeout)
	defer cancel()
	resp, err := s.Web.Search(callCtx, WebSearchQuery{Topic: topic, Limit: cfg.WebLimit})
	if err != nil {
		return SourceStatusEntry{Source: name, Status: statusForError(err), Reason: errorReason(err)}, []WebContext{}
	}
	status := domain.SourceAvailable
	if resp.Status == WebSearchDegraded || resp.Status == WebSearchUnavailable || resp.Status == WebSearchSkipped {
		status = domain.SourceStatus(string(resp.Status))
	}
	contexts := resp.Contexts
	if contexts == nil {
		contexts = []WebContext{}
	}
	return SourceStatusEntry{Source: name, Status: status, Reason: strings.TrimSpace(resp.Reason)}, contexts
}

func webStatusEntry(status WebSearchStatus, reason string) SourceStatusEntry {
	return SourceStatusEntry{Source: "qwen_web_search", Status: domain.SourceStatus(string(status)), Reason: reason}
}

// statusForError maps provider errors onto the frozen source status enum.
func statusForError(err error) domain.SourceStatus {
	switch {
	case err == nil:
		return domain.SourceAvailable
	case IsProviderError(err, ProviderRateLimited),
		IsProviderError(err, ProviderTimeout),
		IsProviderError(err, ProviderTimeoutBudget),
		IsProviderError(err, ProviderInvalidResp):
		return domain.SourceDegraded
	case IsProviderError(err, ProviderNotImplemented):
		return domain.SourceSkipped
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return domain.SourceDegraded
	default:
		return domain.SourceUnavailable
	}
}
