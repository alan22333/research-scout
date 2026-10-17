package discovery

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"research-scout/internal/domain"
)

type fakeSource struct {
	name  DiscoverySource
	calls int32
	works []ExternalWork
	err   error
	delay time.Duration
}

func (f *fakeSource) Name() DiscoverySource { return f.name }

func (f *fakeSource) Search(ctx context.Context, q SearchQuery) (DiscoveryResponse, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return DiscoveryResponse{}, ctx.Err()
		}
	}
	if f.err != nil {
		return DiscoveryResponse{Source: f.name, Status: domain.SourceDegraded}, f.err
	}
	return DiscoveryResponse{Works: f.works, Source: f.name, Status: domain.SourceAvailable}, nil
}

func (f *fakeSource) Get(context.Context, string) (ExternalWork, error) {
	return ExternalWork{}, errors.New("not implemented")
}

func (f *fakeSource) Citations(context.Context, string) ([]CitationRelation, error) {
	return nil, errors.New("not implemented")
}

type fakeWeb struct {
	calls    int32
	contexts []WebContext
	status   WebSearchStatus
	err      error
}

func (f *fakeWeb) Name() string { return "qwen_web_search" }

func (f *fakeWeb) Search(ctx context.Context, q WebSearchQuery) (WebSearchResponse, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.err != nil {
		return WebSearchResponse{Status: WebSearchUnavailable}, f.err
	}
	status := f.status
	if status == "" {
		status = WebSearchAvailable
	}
	return WebSearchResponse{Contexts: f.contexts, Status: status}, nil
}

func TestModeSourceMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode       domain.RetrievalMode
		structured bool
		web        bool
	}{
		{domain.RetrievalModeMyLibrary, false, false},
		{domain.RetrievalModeGapFill, true, false},
		{domain.RetrievalModeLandscape, true, true},
	}
	for _, tc := range cases {
		if got := ModeAllowsStructured(tc.mode); got != tc.structured {
			t.Fatalf("ModeAllowsStructured(%s)=%v", tc.mode, got)
		}
		if got := ModeAllowsWebSearch(tc.mode); got != tc.web {
			t.Fatalf("ModeAllowsWebSearch(%s)=%v", tc.mode, got)
		}
	}
}

func TestDiscoverMyLibraryNeverCallsExternalSources(t *testing.T) {
	t.Parallel()
	s2 := &fakeSource{name: SourceSemanticScholar, works: []ExternalWork{{DOI: "10.1/a", Title: "A", Year: 2020}}}
	oa := &fakeSource{name: SourceOpenAlex}
	web := &fakeWeb{}
	svc := &Service{Sources: []ScholarlyDiscoverySource{s2, oa}, Web: web, Config: ServiceConfig{DiscoveryEnabled: true, WebSearchEnabled: true}}

	res, err := svc.Discover(context.Background(), ServiceRequest{Mode: domain.RetrievalModeMyLibrary, Topic: "topic"})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if s2.calls != 0 || oa.calls != 0 || web.calls != 0 {
		t.Fatalf("my_library must not call external sources: s2=%d oa=%d web=%d", s2.calls, oa.calls, web.calls)
	}
	for _, st := range res.SourceStatus {
		if st.Status != domain.SourceSkipped {
			t.Fatalf("status=%+v want skipped", st)
		}
	}
	if len(res.Gaps) != 0 || len(res.Works) != 0 {
		t.Fatalf("my_library must not emit external works or gaps: %+v", res)
	}
}

func TestDiscoverGapFillCallsStructuredButNotWeb(t *testing.T) {
	t.Parallel()
	s2 := &fakeSource{name: SourceSemanticScholar, works: []ExternalWork{
		{DOI: "10.1/survey", Title: "A survey of differential privacy", Year: 2019, Type: "review", CitationCount: 400},
	}}
	oa := &fakeSource{name: SourceOpenAlex}
	web := &fakeWeb{}
	svc := &Service{Sources: []ScholarlyDiscoverySource{s2, oa}, Web: web, Config: ServiceConfig{DiscoveryEnabled: true, WebSearchEnabled: true}}

	res, err := svc.Discover(context.Background(), ServiceRequest{
		Mode: domain.RetrievalModeGapFill, Topic: "differential privacy",
		Seeds: []LibraryDoc{{DocumentID: "seed", Title: "Differential privacy for federated learning", Year: 2021}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if s2.calls != 1 || oa.calls != 1 {
		t.Fatalf("structured sources must be called: s2=%d oa=%d", s2.calls, oa.calls)
	}
	if web.calls != 0 {
		t.Fatalf("gap_fill must not call web search")
	}
	if len(res.Works) != 1 || len(res.Gaps) != 1 || res.Gaps[0].Type != GapCanonical {
		t.Fatalf("res=%+v", res)
	}
	found := false
	for _, st := range res.SourceStatus {
		if st.Source == "qwen_web_search" && st.Status == domain.SourceSkipped {
			found = true
		}
	}
	if !found {
		t.Fatalf("web search status must be visibly skipped: %+v", res.SourceStatus)
	}
}

func TestDiscoverLandscapeSkipsWebWhenStructuredCoverageSufficient(t *testing.T) {
	t.Parallel()
	s2 := &fakeSource{name: SourceSemanticScholar, works: []ExternalWork{
		{DOI: "10.1/known", Title: "Known work", Year: 2020, Type: "article", CitationCount: 10},
	}}
	web := &fakeWeb{}
	svc := &Service{Sources: []ScholarlyDiscoverySource{s2}, Web: web, Config: ServiceConfig{DiscoveryEnabled: true, WebSearchEnabled: true}}

	res, err := svc.Discover(context.Background(), ServiceRequest{
		Mode: domain.RetrievalModeLandscape, Topic: "known work",
		Library: []LibraryDoc{{DocumentID: "doc", DOI: "10.1/known", Title: "Known work", Year: 2020, InScope: true}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if web.calls != 0 {
		t.Fatalf("web search must be skipped when there are no gaps")
	}
	if res.WebSkippedReason != "structured_coverage_sufficient" {
		t.Fatalf("reason=%q", res.WebSkippedReason)
	}
}

func TestDiscoverLandscapeCallsWebWhenGapsRemain(t *testing.T) {
	t.Parallel()
	s2 := &fakeSource{name: SourceSemanticScholar, works: []ExternalWork{
		{DOI: "10.1/survey", Title: "Survey of differential privacy", Year: 2019, Type: "review", CitationCount: 400},
	}}
	web := &fakeWeb{contexts: []WebContext{{Title: "Blog", URL: "https://example.org/a", Snippet: "snippet"}}}
	svc := &Service{Sources: []ScholarlyDiscoverySource{s2}, Web: web, Config: ServiceConfig{DiscoveryEnabled: true, WebSearchEnabled: true}}

	res, err := svc.Discover(context.Background(), ServiceRequest{
		Mode: domain.RetrievalModeLandscape, Topic: "differential privacy", DeidentifiedTopic: "differential privacy",
		Seeds: []LibraryDoc{{DocumentID: "seed", Title: "Differential privacy for federated learning", Year: 2021}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if web.calls != 1 {
		t.Fatalf("web calls=%d want 1", web.calls)
	}
	if len(res.WebContexts) != 1 || res.WebContexts[0].URL != "https://example.org/a" {
		t.Fatalf("web contexts=%+v", res.WebContexts)
	}
	if len(res.Gaps) == 0 {
		t.Fatalf("expected gaps to justify web search")
	}
}

func TestDiscoverDegradesOneSourceWithoutBlockingOther(t *testing.T) {
	t.Parallel()
	s2 := &fakeSource{name: SourceSemanticScholar, err: NewProviderError("semantic_scholar", ProviderRateLimited, "429")}
	oa := &fakeSource{name: SourceOpenAlex, works: []ExternalWork{
		{DOI: "10.1/survey", Title: "Survey of differential privacy", Year: 2019, Type: "review", CitationCount: 400},
	}}
	svc := &Service{Sources: []ScholarlyDiscoverySource{s2, oa}, Config: ServiceConfig{DiscoveryEnabled: true}}

	res, err := svc.Discover(context.Background(), ServiceRequest{
		Mode: domain.RetrievalModeGapFill, Topic: "differential privacy",
		Seeds: []LibraryDoc{{DocumentID: "seed", Title: "Differential privacy for federated learning", Year: 2021}},
	})
	if err != nil {
		t.Fatalf("partial failure must not fail the run: %v", err)
	}
	if len(res.Gaps) != 1 {
		t.Fatalf("gaps=%+v", res.Gaps)
	}
	var s2Status, oaStatus *SourceStatusEntry
	for i := range res.SourceStatus {
		switch res.SourceStatus[i].Source {
		case "semantic_scholar":
			s2Status = &res.SourceStatus[i]
		case "openalex":
			oaStatus = &res.SourceStatus[i]
		}
	}
	if s2Status == nil || s2Status.Status != domain.SourceDegraded || s2Status.Reason == "" {
		t.Fatalf("s2 status=%+v", s2Status)
	}
	if oaStatus == nil || oaStatus.Status != domain.SourceAvailable {
		t.Fatalf("oa status=%+v", oaStatus)
	}
}

func TestDiscoverEmptyTopicSkipsSources(t *testing.T) {
	t.Parallel()
	s2 := &fakeSource{name: SourceSemanticScholar}
	svc := &Service{Sources: []ScholarlyDiscoverySource{s2}, Config: ServiceConfig{DiscoveryEnabled: true}}
	res, err := svc.Discover(context.Background(), ServiceRequest{Mode: domain.RetrievalModeGapFill, Topic: "   "})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if s2.calls != 0 {
		t.Fatalf("empty topic must not call providers")
	}
	if len(res.SourceStatus) == 0 || res.SourceStatus[0].Reason != "empty_query" {
		t.Fatalf("status=%+v", res.SourceStatus)
	}
}

func TestDiscoverWebUnavailableStillReturnsStructuredResults(t *testing.T) {
	t.Parallel()
	s2 := &fakeSource{name: SourceSemanticScholar, works: []ExternalWork{
		{DOI: "10.1/survey", Title: "Survey of differential privacy", Year: 2019, Type: "review", CitationCount: 400},
	}}
	web := &fakeWeb{status: WebSearchUnavailable, err: NewProviderError("qwen_web_search", ProviderUnauthorized, "no key")}
	svc := &Service{Sources: []ScholarlyDiscoverySource{s2}, Web: web, Config: ServiceConfig{DiscoveryEnabled: true, WebSearchEnabled: true}}
	res, err := svc.Discover(context.Background(), ServiceRequest{
		Mode: domain.RetrievalModeLandscape, Topic: "differential privacy",
		Seeds: []LibraryDoc{{DocumentID: "seed", Title: "Differential privacy for federated learning", Year: 2021}},
	})
	if err != nil {
		t.Fatalf("web failure must not fail the run: %v", err)
	}
	if len(res.Gaps) != 1 {
		t.Fatalf("gaps=%+v", res.Gaps)
	}
	var webStatus *SourceStatusEntry
	for i := range res.SourceStatus {
		if res.SourceStatus[i].Source == "qwen_web_search" {
			webStatus = &res.SourceStatus[i]
		}
	}
	if webStatus == nil || webStatus.Status != domain.SourceUnavailable || webStatus.Reason == "" {
		t.Fatalf("web status=%+v", webStatus)
	}
}

func TestDiscoverDiscoveryDisabledSkipsStructured(t *testing.T) {
	t.Parallel()
	s2 := &fakeSource{name: SourceSemanticScholar}
	svc := &Service{Sources: []ScholarlyDiscoverySource{s2}, Config: ServiceConfig{DiscoveryEnabled: false}}
	res, err := svc.Discover(context.Background(), ServiceRequest{Mode: domain.RetrievalModeLandscape, Topic: "topic"})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if s2.calls != 0 {
		t.Fatalf("disabled discovery must not call providers")
	}
	if res.SourceStatus[0].Reason != "discovery_disabled" {
		t.Fatalf("status=%+v", res.SourceStatus)
	}
}
