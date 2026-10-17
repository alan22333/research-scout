package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const openAlexWorksPath = "/works"

// OpenAlexConfig configures the OpenAlex works adapter.
type OpenAlexConfig struct {
	BaseURL  string
	Email    string
	MaxPages int
	PerPage  int
	Timeout  time.Duration
	Client   *http.Client
}

// OpenAlexSource implements ScholarlyDiscoverySource over the OpenAlex works API.
type OpenAlexSource struct {
	cfg OpenAlexConfig
}

func NewOpenAlexSource(cfg OpenAlexConfig) *OpenAlexSource {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openalex.org"
	}
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = 5
	}
	if cfg.PerPage <= 0 || cfg.PerPage > 200 {
		cfg.PerPage = 50
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	return &OpenAlexSource{cfg: cfg}
}

func (o *OpenAlexSource) Name() DiscoverySource { return SourceOpenAlex }

type openAlexListResponse struct {
	Meta struct {
		Count      int    `json:"count"`
		NextCursor string `json:"next_cursor"`
	} `json:"meta"`
	Results []openAlexWork `json:"results"`
}

type openAlexWork struct {
	ID                    string               `json:"id"`
	DOI                   string               `json:"doi"`
	Title                 string               `json:"title"`
	DisplayName           string               `json:"display_name"`
	PublicationYear       int                  `json:"publication_year"`
	CitedByCount          int                  `json:"cited_by_count"`
	Type                  string               `json:"type"`
	AbstractInvertedIndex map[string][]int     `json:"abstract_inverted_index"`
	Authors               []openAlexAuthorship `json:"authorships"`
	PrimaryLocation       *struct {
		Source *struct {
			DisplayName string `json:"display_name"`
		} `json:"source"`
	} `json:"primary_location"`
}

type openAlexAuthorship struct {
	Author struct {
		DisplayName string `json:"display_name"`
	} `json:"author"`
}

func (o *OpenAlexSource) Search(ctx context.Context, q SearchQuery) (DiscoveryResponse, error) {
	topic := strings.TrimSpace(q.Topic)
	if topic == "" {
		return DiscoveryResponse{Source: SourceOpenAlex, Status: sourceStatusDegraded, Reason: "empty_query"}, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = o.cfg.PerPage
	}
	if limit > o.cfg.PerPage {
		limit = o.cfg.PerPage
	}
	values := url.Values{}
	values.Set("search", topic)
	values.Set("per-page", strconv.Itoa(limit))
	values.Set("select", "id,doi,title,display_name,publication_year,cited_by_count,type,abstract_inverted_index,authorships,primary_location")
	if o.cfg.Email != "" {
		values.Set("mailto", o.cfg.Email)
	}
	var filters []string
	if q.YearFrom > 0 {
		filters = append(filters, fmt.Sprintf("from_publication_date:%d-01-01", q.YearFrom))
	}
	if q.YearTo > 0 {
		filters = append(filters, fmt.Sprintf("to_publication_date:%d-12-31", q.YearTo))
	}
	if len(filters) > 0 {
		values.Set("filter", strings.Join(filters, ","))
	}
	if q.Cursor != "" {
		values.Set("cursor", q.Cursor)
	}
	endpoint := strings.TrimRight(o.cfg.BaseURL, "/") + openAlexWorksPath + "?" + values.Encode()

	var body openAlexListResponse
	if err := o.getJSON(ctx, endpoint, &body); err != nil {
		return DiscoveryResponse{Source: SourceOpenAlex, Status: sourceStatusUnavailable, Reason: errorReason(err)}, err
	}

	works := make([]ExternalWork, 0, len(body.Results))
	for _, item := range body.Results {
		work := normalizeOpenAlexWork(item)
		if work.Status == ExternalStatusDegraded {
			// A single malformed abstract must not poison the whole page.
			work.Status = ExternalStatusDegraded
		}
		works = append(works, work)
	}
	return DiscoveryResponse{
		Works:      works,
		Source:     SourceOpenAlex,
		Status:     sourceStatusAvailable,
		NextCursor: body.Meta.NextCursor,
	}, nil
}

func normalizeOpenAlexWork(item openAlexWork) ExternalWork {
	title := cleanText(item.Title)
	if title == "" {
		title = cleanText(item.DisplayName)
	}
	abstract, abstractErr := restoreAbstract(item.AbstractInvertedIndex)
	venue := ""
	if item.PrimaryLocation != nil && item.PrimaryLocation.Source != nil {
		venue = cleanText(item.PrimaryLocation.Source.DisplayName)
	}
	authors := make([]string, 0, len(item.Authors))
	for _, a := range item.Authors {
		if name := cleanText(a.Author.DisplayName); name != "" {
			authors = append(authors, name)
		}
	}
	w := ExternalWork{
		DOI:           NormalizeDOI(item.DOI),
		OpenAlexID:    normalizeOpenAlexID(item.ID),
		Title:         title,
		Abstract:      abstract,
		Year:          item.PublicationYear,
		Venue:         venue,
		Authors:       authors,
		CitationCount: item.CitedByCount,
		Type:          strings.ToLower(strings.TrimSpace(item.Type)),
		Source:        SourceOpenAlex,
		Sources:       []DiscoverySource{SourceOpenAlex},
		Status:        ExternalStatusAvailable,
		RetrievedAt:   time.Now().UTC(),
	}
	if abstractErr != nil {
		w.Status = ExternalStatusDegraded
	}
	w.IdentityKey = Key(w)
	w.ID = w.IdentityKey
	return w
}

// restoreAbstract rebuilds the OpenAlex inverted abstract index without
// guessing missing positions. Invalid indices degrade to an empty abstract.
func restoreAbstract(inverted map[string][]int) (string, error) {
	if len(inverted) == 0 {
		return "", nil
	}
	maxPos := -1
	for _, positions := range inverted {
		for _, pos := range positions {
			if pos < 0 {
				return "", errors.New("negative abstract position")
			}
			if pos > maxPos {
				maxPos = pos
			}
		}
	}
	if maxPos < 0 {
		return "", nil
	}
	words := make([]string, maxPos+1)
	filled := make([]bool, maxPos+1)
	for word, positions := range inverted {
		for _, pos := range positions {
			if pos >= len(words) {
				return "", errors.New("abstract position out of range")
			}
			words[pos] = word
			filled[pos] = true
		}
	}
	for i, ok := range filled {
		if !ok {
			return "", fmt.Errorf("abstract position %d unfilled", i)
		}
	}
	return strings.Join(words, " "), nil
}

func (o *OpenAlexSource) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return NewProviderError("openalex", ProviderInvalidResp, err.Error())
	}
	req.Header.Set("Accept", "application/json")
	resp, err := o.client().Do(req)
	if err != nil {
		return NewProviderError("openalex", ProviderUnavailable, err.Error())
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return NewProviderError("openalex", ProviderInvalidResp, err.Error())
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return NewProviderError("openalex", ProviderRateLimited, "rate limited")
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return NewProviderError("openalex", ProviderUnauthorized, "authentication rejected")
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return NewProviderError("openalex", ProviderUnavailable, fmt.Sprintf("upstream status %d", resp.StatusCode))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return NewProviderError("openalex", ProviderInvalidResp, "unparseable response")
	}
	return nil
}

func (o *OpenAlexSource) client() *http.Client {
	if o.cfg.Client != nil {
		return o.cfg.Client
	}
	return &http.Client{Timeout: o.cfg.Timeout}
}

func (o *OpenAlexSource) Get(ctx context.Context, id string) (ExternalWork, error) {
	normalized := normalizeOpenAlexID(id)
	if normalized == "" {
		return ExternalWork{}, NewProviderError("openalex", ProviderInvalidResp, "empty work id")
	}
	values := url.Values{}
	values.Set("select", "id,doi,title,display_name,publication_year,cited_by_count,type,abstract_inverted_index,authorships,primary_location")
	if o.cfg.Email != "" {
		values.Set("mailto", o.cfg.Email)
	}
	endpoint := strings.TrimRight(o.cfg.BaseURL, "/") + openAlexWorksPath + "/" + url.PathEscape(normalized) + "?" + values.Encode()
	var item openAlexWork
	if err := o.getJSON(ctx, endpoint, &item); err != nil {
		return ExternalWork{}, err
	}
	return normalizeOpenAlexWork(item), nil
}

func (o *OpenAlexSource) Citations(ctx context.Context, id string) ([]CitationRelation, error) {
	return nil, NewProviderError("openalex", ProviderNotImplemented, "citation graph expansion is out of scope for stage 3")
}
