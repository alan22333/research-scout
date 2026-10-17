// Package retrieval contains deterministic fusion, selection, and optional
// reranking primitives shared by the Stage 2 query engine.
package retrieval

import (
	"context"
	"errors"
	"sort"
	"strings"

	"research-scout/internal/domain"
)

type Source string

const (
	SourceLexical Source = "lexical"
	SourceVector  Source = "vector"
)

const DefaultRRFK = 60

// FallbackRerankVersion is the stable version identifier used when the
// optional reranker is absent or fails. It keeps evaluation reports comparable
// across environments that intentionally use the deterministic fallback.
const FallbackRerankVersion = "rrf-fallback-v1"

// RankedList is one weighted ranked list before fusion.
type RankedList struct {
	QueryWeight  float64
	SourceWeight float64
	Source       Source
	Candidates   []Candidate
}

// Candidate is a chunk-level retrieval candidate. Rank fields are populated
// during fusion and are one-based.
type Candidate struct {
	ChunkID           string
	DocumentID        string
	DocumentVersionID string
	Title             string
	Section           string
	PageStart         *int
	PageEnd           *int
	CharStart         int
	CharEnd           int
	Content           string
	TokenCount        int
	EvidenceType      domain.EvidenceType
	FusedScore        float64
	VectorScore       float64
	LexicalRank       int
	VectorRank        int
	Source            Source
}

// FuseRRF merges duplicate chunk identities using weighted reciprocal rank
// fusion. A chunk appearing in both lists is one candidate, not two.
func FuseRRF(lists []RankedList, k int) []Candidate {
	if k <= 0 {
		k = DefaultRRFK
	}
	merged := map[string]Candidate{}
	for _, list := range lists {
		for i, source := range list.Candidates {
			rank := i + 1
			if strings.TrimSpace(source.ChunkID) == "" {
				continue
			}
			current, ok := merged[source.ChunkID]
			if !ok {
				current = source
			}
			score := list.QueryWeight * list.SourceWeight / float64(k+rank)
			current.FusedScore += score
			if list.Source == SourceLexical && (current.LexicalRank == 0 || rank < current.LexicalRank) {
				current.LexicalRank = rank
			}
			if list.Source == SourceVector && (current.VectorRank == 0 || rank < current.VectorRank) {
				current.VectorRank = rank
			}
			if current.Source == "" {
				current.Source = list.Source
			}
			merged[source.ChunkID] = current
		}
	}
	out := make([]Candidate, 0, len(merged))
	for _, candidate := range merged {
		out = append(out, candidate)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].FusedScore != out[j].FusedScore {
			return out[i].FusedScore > out[j].FusedScore
		}
		if out[i].DocumentID != out[j].DocumentID {
			return out[i].DocumentID < out[j].DocumentID
		}
		return out[i].ChunkID < out[j].ChunkID
	})
	return out
}

// SelectionConfig bounds the evidence set after fusion.
type SelectionConfig struct {
	MaxChunks      int
	MaxDocuments   int
	PerDocumentCap int
	TokenBudget    int
}

// Select applies chunk, document, per-document and token caps in score order.
func Select(candidates []Candidate, cfg SelectionConfig) []Candidate {
	if cfg.MaxChunks <= 0 {
		cfg.MaxChunks = 12
	}
	if cfg.MaxDocuments <= 0 {
		cfg.MaxDocuments = 6
	}
	if cfg.PerDocumentCap <= 0 {
		cfg.PerDocumentCap = 2
	}
	if cfg.TokenBudget <= 0 {
		cfg.TokenBudget = 12000
	}
	ordered := append([]Candidate(nil), candidates...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].FusedScore != ordered[j].FusedScore {
			return ordered[i].FusedScore > ordered[j].FusedScore
		}
		if ordered[i].DocumentID != ordered[j].DocumentID {
			return ordered[i].DocumentID < ordered[j].DocumentID
		}
		return ordered[i].ChunkID < ordered[j].ChunkID
	})
	out := make([]Candidate, 0, cfg.MaxChunks)
	docCounts := map[string]int{}
	tokens := 0
	for _, candidate := range ordered {
		if len(out) >= cfg.MaxChunks {
			break
		}
		if candidate.DocumentID == "" {
			continue
		}
		if docCounts[candidate.DocumentID] >= cfg.PerDocumentCap {
			continue
		}
		if docCounts[candidate.DocumentID] == 0 && len(docCounts) >= cfg.MaxDocuments {
			continue
		}
		if candidate.TokenCount > 0 && tokens+candidate.TokenCount > cfg.TokenBudget {
			continue
		}
		docCounts[candidate.DocumentID]++
		tokens += candidate.TokenCount
		out = append(out, candidate)
	}
	return out
}

// RerankProvider is an optional boundary for a future cross-encoder reranker.
type RerankProvider interface {
	Rerank(context.Context, string, []Candidate) ([]Candidate, error)
	Version() string
}

// RerankOutcome records whether reranking was used and why it was bypassed.
type RerankOutcome struct {
	Used           bool
	Provider       string
	Version        string
	DegradedReason string
}

// RerankOrFallback preserves the fused ordering whenever no provider exists or
// the provider fails. Retrieval must not fail merely because reranking is
// unavailable.
func RerankOrFallback(ctx context.Context, provider RerankProvider, query string, candidates []Candidate) ([]Candidate, RerankOutcome) {
	fallback := append([]Candidate(nil), candidates...)
	if provider == nil {
		return fallback, RerankOutcome{DegradedReason: "rerank_provider_unavailable"}
	}
	version := provider.Version()
	reranked, err := provider.Rerank(ctx, query, append([]Candidate(nil), candidates...))
	if err != nil {
		return fallback, RerankOutcome{Provider: "reranker", Version: version, DegradedReason: "rerank_provider_error: " + err.Error()}
	}
	if len(reranked) == 0 && len(candidates) > 0 {
		return fallback, RerankOutcome{Provider: "reranker", Version: version, DegradedReason: "rerank_provider_empty"}
	}
	return reranked, RerankOutcome{Used: true, Provider: "reranker", Version: version}
}

var _ = errors.New
