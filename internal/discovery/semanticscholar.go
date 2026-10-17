package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const semanticScholarBulkPath = "/graph/v1/paper/search/bulk"

// SemanticScholarConfig configures the Semantic Scholar bulk search adapter.
type SemanticScholarConfig struct {
	BaseURL        string
	APIKey         string
	RequestsPerSec int
	MaxRetries     int
	Timeout        time.Duration
	MaxPages       int
	Client         *http.Client
}

// SemanticScholarSource implements ScholarlyDiscoverySource over the
// Semantic Scholar Graph API bulk search endpoint.
type SemanticScholarSource struct {
	cfg SemanticScholarConfig
	rl  *rateLimiter
}

func NewSemanticScholarSource(cfg SemanticScholarConfig) *SemanticScholarSource {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.semanticscholar.org"
	}
	if cfg.RequestsPerSec <= 0 {
		cfg.RequestsPerSec = 1
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 4
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = 5
	}
	return &SemanticScholarSource{cfg: cfg, rl: newRateLimiter(cfg.RequestsPerSec)}
}

func (s *SemanticScholarSource) Name() DiscoverySource { return SourceSemanticScholar }

type s2BulkResponse struct {
	Total  int       `json:"total"`
	Offset int       `json:"offset"`
	Next   int       `json:"next"`
	Token  string    `json:"token"`
	Data   []s2Paper `json:"data"`
}

type s2Paper struct {
	PaperID       string         `json:"paperId"`
	ExternalIDs   map[string]any `json:"externalIds"`
	Title         string         `json:"title"`
	Abstract      string         `json:"abstract"`
	Year          int            `json:"year"`
	Venue         string         `json:"venue"`
	CitationCount int            `json:"citationCount"`
	Types         []string       `json:"publicationTypes"`
}

func (s *SemanticScholarSource) Search(ctx context.Context, q SearchQuery) (DiscoveryResponse, error) {
	topic := strings.TrimSpace(q.Topic)
	if topic == "" {
		return DiscoveryResponse{Source: SourceSemanticScholar, Status: sourceStatusDegraded, Reason: "empty_query"}, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 25
	}
	fields := "paperId,externalIds,title,abstract,year,venue,citationCount,publicationTypes"
	values := url.Values{}
	values.Set("query", topic)
	values.Set("fields", fields)
	values.Set("limit", strconv.Itoa(limit))
	if q.YearFrom > 0 {
		values.Set("year", fmt.Sprintf("%d-%d", q.YearFrom, q.YearTo))
	}
	if q.Cursor != "" {
		values.Set("token", q.Cursor)
	}
	endpoint := strings.TrimRight(s.cfg.BaseURL, "/") + semanticScholarBulkPath + "?" + values.Encode()

	var body s2BulkResponse
	if err := s.getJSON(ctx, endpoint, &body); err != nil {
		return DiscoveryResponse{Source: SourceSemanticScholar, Status: sourceStatusUnavailable, Reason: errorReason(err)}, err
	}
	works := make([]ExternalWork, 0, len(body.Data))
	for _, p := range body.Data {
		works = append(works, normalizeS2Paper(p))
	}
	next := ""
	if body.Token != "" {
		next = body.Token
	} else if body.Next > 0 {
		next = strconv.Itoa(body.Next)
	}
	return DiscoveryResponse{
		Works:      works,
		Source:     SourceSemanticScholar,
		Status:     sourceStatusAvailable,
		NextCursor: next,
	}, nil
}

func normalizeS2Paper(p s2Paper) ExternalWork {
	doi := ""
	pmid := ""
	for k, v := range p.ExternalIDs {
		switch strings.ToLower(k) {
		case "doi":
			doi = NormalizeDOI(asString(v))
		case "pubmed":
			pmid = strings.ToLower(strings.TrimSpace(asString(v)))
		}
	}
	workType := ""
	if len(p.Types) > 0 {
		workType = strings.ToLower(strings.TrimSpace(p.Types[0]))
	}
	w := ExternalWork{
		DOI:           doi,
		S2ID:          strings.TrimSpace(p.PaperID),
		PMID:          pmid,
		Title:         cleanText(p.Title),
		Abstract:      cleanText(p.Abstract),
		Year:          p.Year,
		Venue:         cleanText(p.Venue),
		CitationCount: p.CitationCount,
		Type:          workType,
		Source:        SourceSemanticScholar,
		Sources:       []DiscoverySource{SourceSemanticScholar},
		Status:        ExternalStatusAvailable,
		RetrievedAt:   time.Now().UTC(),
	}
	w.IdentityKey = Key(w)
	w.ID = w.IdentityKey
	return w
}

func (s *SemanticScholarSource) getJSON(ctx context.Context, endpoint string, out any) error {
	retries := s.cfg.MaxRetries
	for attempt := 0; ; attempt++ {
		if err := s.rl.Wait(ctx); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return NewProviderError("semantic_scholar", ProviderInvalidResp, err.Error())
		}
		if s.cfg.APIKey != "" {
			req.Header.Set("x-api-key", s.cfg.APIKey)
		}
		req.Header.Set("Accept", "application/json")
		resp, err := s.client().Do(req)
		if err != nil {
			if attempt < retries {
				continue
			}
			return NewProviderError("semantic_scholar", ProviderUnavailable, err.Error())
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if readErr != nil {
			return NewProviderError("semantic_scholar", ProviderInvalidResp, readErr.Error())
		}
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			if attempt < retries {
				s.sleepBackoff(ctx, attempt, resp.Header.Get("Retry-After"))
				if ctx.Err() != nil {
					return ctx.Err()
				}
				continue
			}
			return NewProviderError("semantic_scholar", ProviderRateLimited, "rate limited after retries")
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return NewProviderError("semantic_scholar", ProviderUnauthorized, "authentication rejected")
		case resp.StatusCode >= 500:
			if attempt < retries {
				s.sleepBackoff(ctx, attempt, resp.Header.Get("Retry-After"))
				if ctx.Err() != nil {
					return ctx.Err()
				}
				continue
			}
			return NewProviderError("semantic_scholar", ProviderUnavailable, fmt.Sprintf("upstream status %d", resp.StatusCode))
		case resp.StatusCode < 200 || resp.StatusCode >= 300:
			return NewProviderError("semantic_scholar", ProviderInvalidResp, fmt.Sprintf("upstream status %d", resp.StatusCode))
		}
		if err := json.Unmarshal(sanitizeJSONControlBytes(data), out); err != nil {
			return NewProviderError("semantic_scholar", ProviderInvalidResp, "unparseable response")
		}
		return nil
	}
}

func (s *SemanticScholarSource) sleepBackoff(ctx context.Context, attempt int, retryAfter string) {
	delay := parseRetryAfter(retryAfter)
	if delay <= 0 {
		delay = time.Duration(1<<uint(attempt)) * 50 * time.Millisecond
	}
	if delay > 2*time.Second {
		delay = 2 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (s *SemanticScholarSource) client() *http.Client {
	if s.cfg.Client != nil {
		return s.cfg.Client
	}
	return &http.Client{Timeout: s.cfg.Timeout}
}

func parseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if ts, err := http.ParseTime(raw); err == nil {
		if d := time.Until(ts); d > 0 {
			return d
		}
	}
	return 0
}

func (s *SemanticScholarSource) Get(ctx context.Context, id string) (ExternalWork, error) {
	res, err := s.Search(ctx, SearchQuery{Topic: id, Limit: 1})
	if err != nil {
		return ExternalWork{}, err
	}
	for _, w := range res.Works {
		if strings.EqualFold(w.S2ID, id) || strings.EqualFold(w.DOI, NormalizeDOI(id)) {
			return w, nil
		}
	}
	if len(res.Works) > 0 {
		return res.Works[0], nil
	}
	return ExternalWork{}, NewProviderError("semantic_scholar", ProviderInvalidResp, "work not found")
}

func (s *SemanticScholarSource) Citations(ctx context.Context, id string) ([]CitationRelation, error) {
	return nil, NewProviderError("semantic_scholar", ProviderNotImplemented, "citation graph expansion is out of scope for stage 3")
}
