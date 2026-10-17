// Package discovery implements external scholarly discovery, external work
// normalization, library de-duplication and research gap detection.
package discovery

import (
	"context"
	"errors"
	"time"

	"research-scout/internal/domain"
)

// ProviderErrorCode is a stable, provider-agnostic external call failure class.
type ProviderErrorCode string

const (
	ProviderUnavailable    ProviderErrorCode = "unavailable"
	ProviderRateLimited    ProviderErrorCode = "rate_limited"
	ProviderUnauthorized   ProviderErrorCode = "unauthorized"
	ProviderInvalidResp    ProviderErrorCode = "invalid_response"
	ProviderTimeout        ProviderErrorCode = "timeout"
	ProviderTimeoutBudget  ProviderErrorCode = "timeout_budget"
	ProviderNotImplemented ProviderErrorCode = "not_implemented"
)

// ProviderError is the only error type that may cross the discovery boundary.
type ProviderError struct {
	Provider  string
	Code      ProviderErrorCode
	Message   string
	Retryable bool
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	if e.Provider == "" {
		return string(e.Code) + ": " + e.Message
	}
	return e.Provider + "/" + string(e.Code) + ": " + e.Message
}

func NewProviderError(provider string, code ProviderErrorCode, message string) *ProviderError {
	return &ProviderError{Provider: provider, Code: code, Message: message, Retryable: code == ProviderRateLimited || code == ProviderUnavailable || code == ProviderTimeout}
}

// IsProviderError reports whether err (or an error it wraps) is a ProviderError with code.
func IsProviderError(err error, code ProviderErrorCode) bool {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.Code == code
	}
	return false
}

// DiscoverySource identifies the structured scholarly provider that produced a work.
type DiscoverySource string

const (
	SourceSemanticScholar DiscoverySource = "semantic_scholar"
	SourceOpenAlex        DiscoverySource = "openalex"
)

// ExternalStatus describes the trust level of a normalized external record.
type ExternalStatus string

const (
	ExternalStatusAvailable ExternalStatus = "available"
	ExternalStatusDegraded  ExternalStatus = "degraded"
	ExternalStatusInvalid   ExternalStatus = "invalid"
)

// ExternalWork is the provider-agnostic normalized scholarly record.
type ExternalWork struct {
	ID                string
	IdentityKey       string
	DOI               string
	S2ID              string
	OpenAlexID        string
	PMID              string
	Title             string
	Abstract          string
	Year              int
	Venue             string
	Authors           []string
	CitationCount     int
	Type              string
	Source            DiscoverySource
	Sources           []DiscoverySource
	Status            ExternalStatus
	RetrievedAt       time.Time
	AlreadyInLibrary  bool
	LibraryDocumentID string
	InCurrentScope    bool
}

// LibraryDoc is the minimal projection of an indexed personal-library document
// needed for library de-duplication and gap detection.
type LibraryDoc struct {
	DocumentID string
	Title      string
	Year       int
	DOI        string
	InScope    bool
	Missing    bool
}

// CitationRelation is a read-only citation/reference relation between two works.
type CitationRelation struct {
	ExternalWorkID string
	Cites          string
	CitedBy        string
	RetrievedAt    time.Time
}

// SearchQuery is a bounded structured-discovery request.
type SearchQuery struct {
	Topic    string
	Mode     domain.RetrievalMode
	YearFrom int
	YearTo   int
	Cursor   string
	Limit    int
}

// DiscoveryResponse carries per-source status alongside results so callers can
// render degraded coverage instead of silently claiming completeness.
type DiscoveryResponse struct {
	Works      []ExternalWork
	Source     DiscoverySource
	Status     domain.SourceStatus
	Reason     string
	NextCursor string
}

// ScholarlyDiscoverySource searches and retrieves normalized scholarly works.
type ScholarlyDiscoverySource interface {
	Name() DiscoverySource
	Search(ctx context.Context, q SearchQuery) (DiscoveryResponse, error)
	Get(ctx context.Context, id string) (ExternalWork, error)
	Citations(ctx context.Context, id string) ([]CitationRelation, error)
}

// GapType mirrors the frozen domain enum for research gaps.
type GapType string

const (
	GapCanonical      GapType = "canonical"
	GapRelatedMethod  GapType = "related_method"
	GapLatestProgress GapType = "latest_progress"
)

// MissingRelativeTo explains what the gap is measured against.
type MissingRelativeTo string

const (
	MissingPersonalLibrary MissingRelativeTo = "personal_library"
	MissingCurrentScope    MissingRelativeTo = "current_scope"
	MissingExternalOnly    MissingRelativeTo = "external_only"
)

// Gap is a single detected research gap with a human-readable rationale.
type Gap struct {
	Type              GapType
	Work              ExternalWork
	MissingRelativeTo MissingRelativeTo
	Rationale         string
	NotYetInLibrary   bool
}

// GapInput is the deterministic input of the gap detector.
type GapInput struct {
	NowYear            int
	Works              []ExternalWork
	Seeds              []LibraryDoc
	Library            []LibraryDoc
	CanonicalCitations int
	// LibraryUnknown marks a run without a synced My Library projection. Gaps
	// are then reported as external_only instead of personal_library.
	LibraryUnknown bool
}

// WebSearchStatus mirrors domain.SourceStatus but stays local to the provider boundary.
type WebSearchStatus string

const (
	WebSearchAvailable   WebSearchStatus = "available"
	WebSearchDegraded    WebSearchStatus = "degraded"
	WebSearchUnavailable WebSearchStatus = "unavailable"
	WebSearchSkipped     WebSearchStatus = "skipped"
)

// WebSearchQuery is a de-identified topic-only search request.
type WebSearchQuery struct {
	Topic     string
	Freshness string
	Limit     int
}

// WebContext is one web result. URL is mandatory for it to become evidence.
type WebContext struct {
	Title       string
	URL         string
	Snippet     string
	Provider    string
	Model       string
	ResponseID  string
	RetrievedAt time.Time
}

// WebSearchResponse carries normalized web contexts and a visible degradation status.
type WebSearchResponse struct {
	Contexts []WebContext
	Status   WebSearchStatus
	Reason   string
}

// WebSearchProvider is the provider-native web search boundary.
type WebSearchProvider interface {
	Name() string
	Search(ctx context.Context, q WebSearchQuery) (WebSearchResponse, error)
}

// DiscoveryServiceConfiguration bounds external discovery for one query.
type DiscoveryServiceConfiguration struct {
	YearFrom int
	YearTo   int
	Limit    int
	WebLimit int
	NowYear  int
}
