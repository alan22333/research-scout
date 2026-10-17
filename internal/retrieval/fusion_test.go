package retrieval

import (
	"context"
	"errors"
	"testing"

	"research-scout/internal/domain"
)

func TestWeightedRRFMergesDuplicateChunks(t *testing.T) {
	lists := []RankedList{
		{QueryWeight: 1.0, SourceWeight: 1.0, Source: SourceLexical, Candidates: []Candidate{
			{ChunkID: "chunk-a", DocumentID: "doc-1", EvidenceType: domain.EvidencePersonalFulltext},
			{ChunkID: "chunk-b", DocumentID: "doc-1", EvidenceType: domain.EvidencePersonalFulltext},
		}},
		{QueryWeight: 0.5, SourceWeight: 1.0, Source: SourceVector, Candidates: []Candidate{
			{ChunkID: "chunk-a", DocumentID: "doc-1", EvidenceType: domain.EvidencePersonalFulltext},
			{ChunkID: "chunk-c", DocumentID: "doc-2", EvidenceType: domain.EvidenceStructuredAbstract},
		}},
	}
	got := FuseRRF(lists, 60)
	if len(got) != 3 {
		t.Fatalf("fused chunks=%d want 3: %+v", len(got), got)
	}
	if got[0].ChunkID != "chunk-a" {
		t.Fatalf("first chunk=%s want chunk-a", got[0].ChunkID)
	}
	want := 1.0/61.0 + 0.5/61.0
	if diff := got[0].FusedScore - want; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("score=%f want %f", got[0].FusedScore, want)
	}
	if got[0].LexicalRank != 1 || got[0].VectorRank != 1 {
		t.Fatalf("ranks=%d/%d want 1/1", got[0].LexicalRank, got[0].VectorRank)
	}
}

func TestSelectionCapsAndBudget(t *testing.T) {
	candidates := []Candidate{
		{ChunkID: "a1", DocumentID: "doc-a", FusedScore: 10, TokenCount: 100},
		{ChunkID: "a2", DocumentID: "doc-a", FusedScore: 9, TokenCount: 100},
		{ChunkID: "a3", DocumentID: "doc-a", FusedScore: 8, TokenCount: 100},
		{ChunkID: "b1", DocumentID: "doc-b", FusedScore: 7, TokenCount: 100},
		{ChunkID: "c1", DocumentID: "doc-c", FusedScore: 6, TokenCount: 100},
		{ChunkID: "d1", DocumentID: "doc-d", FusedScore: 5, TokenCount: 100},
		{ChunkID: "e1", DocumentID: "doc-e", FusedScore: 4, TokenCount: 100},
	}
	got := Select(candidates, SelectionConfig{MaxChunks: 12, MaxDocuments: 3, PerDocumentCap: 2, TokenBudget: 450})
	if len(got) != 4 {
		t.Fatalf("selected=%d want 4 due to 450 token budget: %+v", len(got), got)
	}
	docs := map[string]int{}
	for _, c := range got {
		docs[c.DocumentID]++
	}
	if docs["doc-a"] > 2 {
		t.Fatalf("doc-a selected %d chunks, cap is 2", docs["doc-a"])
	}
	if len(docs) > 3 {
		t.Fatalf("selected documents=%d want <=3", len(docs))
	}
}

type failingReranker struct{}

func (failingReranker) Rerank(context.Context, string, []Candidate) ([]Candidate, error) {
	return nil, errors.New("provider unavailable")
}
func (failingReranker) Version() string { return "failing-test" }

func TestRerankFallbackKeepsRRF(t *testing.T) {
	in := []Candidate{
		{ChunkID: "a", FusedScore: 0.9, VectorScore: 0.2},
		{ChunkID: "b", FusedScore: 0.8, VectorScore: 0.9},
	}
	got, outcome := RerankOrFallback(context.Background(), failingReranker{}, "query", in)
	if outcome.Used || outcome.DegradedReason == "" {
		t.Fatalf("outcome=%+v want degraded fallback", outcome)
	}
	if len(got) != 2 || got[0].ChunkID != "a" || got[0].FusedScore != 0.9 {
		t.Fatalf("fallback changed RRF order/data: %+v", got)
	}
}
