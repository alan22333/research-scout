package discovery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSemanticScholarRetriesAfterRateLimit(t *testing.T) {
	t.Parallel()
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing api key header")
		}
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":"rate limited"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"total":1,"offset":0,"data":[{"paperId":"S1","externalIds":{"DOI":"10.1/one"},"title":"One","abstract":" Abstract one ",`+"\n"+`"year":2020,"venue":"Venue","citationCount":7}]}`)
	}))
	defer srv.Close()

	src := NewSemanticScholarSource(SemanticScholarConfig{
		BaseURL:        srv.URL,
		APIKey:         "test-key",
		RequestsPerSec: 1000,
		MaxRetries:     3,
		Client:         srv.Client(),
	})
	res, err := src.Search(context.Background(), SearchQuery{Topic: "one", Limit: 10})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Fatalf("attempts=%d", attempts)
	}
	if len(res.Works) != 1 || res.Works[0].S2ID != "S1" || res.Works[0].DOI != "10.1/one" || res.Works[0].Abstract != "Abstract one" {
		t.Fatalf("works=%+v", res.Works)
	}
}

func TestSemanticScholarGivesUpAfterMaxRetries(t *testing.T) {
	t.Parallel()
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":"rate limited"}`)
	}))
	defer srv.Close()

	src := NewSemanticScholarSource(SemanticScholarConfig{
		BaseURL:        srv.URL,
		RequestsPerSec: 1000,
		MaxRetries:     2,
		Client:         srv.Client(),
	})
	_, err := src.Search(context.Background(), SearchQuery{Topic: "one"})
	if err == nil {
		t.Fatalf("want rate limited error")
	}
	if !IsProviderError(err, ProviderRateLimited) {
		t.Fatalf("err=%v want rate_limited", err)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Fatalf("attempts=%d want initial+2 retries", attempts)
	}
}

func TestOpenAlexRestoresInvertedAbstractAndCursor(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mailto") != "test@example.com" {
			t.Errorf("missing polite pool mailto")
		}
		if r.URL.Query().Get("cursor") != "next-token" {
			t.Errorf("cursor=%q", r.URL.Query().Get("cursor"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"meta":{"count":1},"results":[{"id":"https://openalex.org/W1","doi":"https://doi.org/10.1/oa","title":"OA work","display_name":"OA work","publication_year":2021,"cited_by_count":11,"type":"article","primary_location":{"source":{"display_name":"Journal"}},"abstract_inverted_index":{"Abstract":[0],"words":[1]}}]}`)
	}))
	defer srv.Close()

	src := NewOpenAlexSource(OpenAlexConfig{BaseURL: srv.URL, Email: "test@example.com", MaxPages: 1, Client: srv.Client()})
	res, err := src.Search(context.Background(), SearchQuery{Topic: "oa", Cursor: "next-token", Limit: 10})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(res.Works) != 1 || res.Works[0].OpenAlexID != "W1" || res.Works[0].DOI != "10.1/oa" || res.Works[0].Abstract != "Abstract words" {
		t.Fatalf("works=%+v", res.Works)
	}
	if res.Works[0].Venue != "Journal" || res.Works[0].CitationCount != 11 {
		t.Fatalf("works=%+v", res.Works)
	}
}

func TestOpenAlexDegradesOnInvalidAbstractWithoutLosingWork(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"meta":{"count":1},"results":[{"id":"W2","doi":"10.1/bad","title":"Bad abstract","publication_year":2022,"type":"article","abstract_inverted_index":{"a":[1]}}]}`)
	}))
	defer srv.Close()

	src := NewOpenAlexSource(OpenAlexConfig{BaseURL: srv.URL, Email: "test@example.com", MaxPages: 1, Client: srv.Client()})
	res, err := src.Search(context.Background(), SearchQuery{Topic: "bad"})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(res.Works) != 1 || res.Works[0].Abstract != "" {
		t.Fatalf("works=%+v", res.Works)
	}
	if res.Works[0].Status != ExternalStatusDegraded {
		t.Fatalf("status=%s want degraded", res.Works[0].Status)
	}
}

func TestQwenWebSearchRequiresURLCitation(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp-1","choices":[{"message":{"role":"assistant","content":"Background context"}}],"search_info":{"search_results":[{"title":"Source one","url":"https://example.org/one","snippet":"Snippet one"},{"title":"No url","url":"","snippet":"bad"}]}}`)
	}))
	defer srv.Close()

	p := NewQwenWebSearch(QwenWebSearchConfig{BaseURL: srv.URL, APIKey: "test-key", Model: "qwen-plus", Client: srv.Client()})
	res, err := p.Search(context.Background(), WebSearchQuery{Topic: "topic", Limit: 5})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(res.Contexts) != 1 || res.Contexts[0].URL != "https://example.org/one" {
		t.Fatalf("contexts=%+v", res.Contexts)
	}
	if res.Status != WebSearchDegraded {
		t.Fatalf("status=%s want degraded when a result lacked URL", res.Status)
	}
	if res.Contexts[0].RetrievedAt.IsZero() {
		t.Fatalf("retrieved_at must be set")
	}
}

func TestQwenWebSearchWithoutKeyIsUnavailable(t *testing.T) {
	t.Parallel()
	p := NewQwenWebSearch(QwenWebSearchConfig{})
	res, err := p.Search(context.Background(), WebSearchQuery{Topic: "topic"})
	if err != nil {
		t.Fatalf("missing key must degrade, not error: %v", err)
	}
	if res.Status != WebSearchUnavailable || res.Reason != "web_search_unavailable" {
		t.Fatalf("res=%+v", res)
	}
}

func TestDisabledWebSearchSkips(t *testing.T) {
	t.Parallel()
	res, err := DisabledWebSearchProvider{}.Search(context.Background(), WebSearchQuery{Topic: "t"})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if res.Status != WebSearchSkipped {
		t.Fatalf("res=%+v", res)
	}
}

func TestRateLimiterSpacing(t *testing.T) {
	t.Parallel()
	rl := newRateLimiter(20)
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := rl.Wait(context.Background()); err != nil {
			t.Fatalf("err=%v", err)
		}
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("elapsed=%v want at least 80ms", elapsed)
	}
}

// TestSemanticScholarCleansControlCharactersBeforeDecode locks the S2 payload
// quirk: abstracts sometimes carry raw control bytes that are illegal in JSON.
// The adapter must strip them before decoding instead of discarding a valid
// result set as invalid_response.
func TestSemanticScholarCleansControlCharactersBeforeDecode(t *testing.T) {
	t.Parallel()
	// Build a payload with a raw NUL (0x00) and 0x0b inside the abstract string.
	payload := []byte(`{"total":1,"offset":0,"data":[{"paperId":"S9","externalIds":{"DOI":"10.9/nine"},"title":"Nine","abstract":"clean`)
	payload = append(payload, 0x00, 0x0b)
	payload = append(payload, []byte(`text","year":2021,"venue":"V","citationCount":3}]}`)...)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(payload)
	}))
	defer srv.Close()

	src := NewSemanticScholarSource(SemanticScholarConfig{
		BaseURL: srv.URL, APIKey: "k", RequestsPerSec: 1000, MaxRetries: 1, Client: srv.Client(),
	})
	res, err := src.Search(context.Background(), SearchQuery{Topic: "control", Limit: 5})
	if err != nil {
		t.Fatalf("decode with control chars failed: %v", err)
	}
	if len(res.Works) != 1 || res.Works[0].S2ID != "S9" {
		t.Fatalf("works=%+v want the S9 record", res.Works)
	}
	if strings.ContainsAny(res.Works[0].Abstract, "\x00\x0b") {
		t.Fatalf("control characters survived normalization: %q", res.Works[0].Abstract)
	}
}

// TestSemanticScholarDecodesMixedExternalIDs locks the real provider shape
// where externalIds mixes DOI strings with numeric CorpusId/PMID values. A
// naive map[string]string makes every bulk payload fail to decode.
func TestSemanticScholarDecodesMixedExternalIDs(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"total":1,"offset":0,"data":[{"paperId":"S7","externalIds":{"DOI":"10.1/seven","CorpusId":123456,"PubMed":"99887766"},"title":"Seven","abstract":"abs","year":2020,"venue":"V","citationCount":9}]}`)
	}))
	defer srv.Close()

	src := NewSemanticScholarSource(SemanticScholarConfig{
		BaseURL: srv.URL, APIKey: "k", RequestsPerSec: 1000, MaxRetries: 1, Client: srv.Client(),
	})
	res, err := src.Search(context.Background(), SearchQuery{Topic: "mixed", Limit: 5})
	if err != nil {
		t.Fatalf("decode mixed externalIds failed: %v", err)
	}
	if len(res.Works) != 1 {
		t.Fatalf("works=%+v want 1", res.Works)
	}
	if res.Works[0].DOI != "10.1/seven" || res.Works[0].PMID != "99887766" {
		t.Fatalf("doi=%q pmid=%q want 10.1/seven / 99887766", res.Works[0].DOI, res.Works[0].PMID)
	}
}

func TestQwenWebSearchExtractsURLsFromAnswerContent(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp-fallback","choices":[{"message":{"content":"Sources:\nRetrieval Evaluation Overview\n🔗 https://example.org/paper\nAnother Source\n🔗 https://example.com/report。"}}]}`)
	}))
	defer srv.Close()

	p := NewQwenWebSearch(QwenWebSearchConfig{BaseURL: srv.URL, APIKey: "test-key", Model: "qwen-plus", Client: srv.Client()})
	res, err := p.Search(context.Background(), WebSearchQuery{Topic: "topic", Limit: 5})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(res.Contexts) != 2 {
		t.Fatalf("contexts=%+v want 2", res.Contexts)
	}
	if res.Status != WebSearchDegraded || res.Reason != "web_search_content_fallback" {
		t.Fatalf("status=%s reason=%s want degraded/content fallback", res.Status, res.Reason)
	}
	if res.Contexts[0].URL != "https://example.org/paper" {
		t.Fatalf("url=%q", res.Contexts[0].URL)
	}
	if res.Contexts[0].Title != "Retrieval Evaluation Overview" {
		t.Fatalf("title=%q", res.Contexts[0].Title)
	}
	if res.Contexts[1].URL != "https://example.com/report" {
		t.Fatalf("url=%q want punctuation trimmed", res.Contexts[1].URL)
	}
}

func TestQwenWebSearchExtractsMarkdownLinkTitle(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp-md","choices":[{"message":{"content":"1. [Grounded RAG](https://example.org/rag) - useful overview"}}]}`)
	}))
	defer srv.Close()

	p := NewQwenWebSearch(QwenWebSearchConfig{BaseURL: srv.URL, APIKey: "test-key", Model: "qwen-plus", Client: srv.Client()})
	res, err := p.Search(context.Background(), WebSearchQuery{Topic: "topic", Limit: 5})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(res.Contexts) != 1 {
		t.Fatalf("contexts=%+v want 1", res.Contexts)
	}
	if res.Contexts[0].Title != "Grounded RAG" || res.Contexts[0].URL != "https://example.org/rag" {
		t.Fatalf("context=%+v", res.Contexts[0])
	}
}
