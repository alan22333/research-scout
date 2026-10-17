package eval

import "testing"

func TestRetrievalMetrics(t *testing.T) {
	ranked := []string{"x", "a", "b", "c"}
	relevant := map[string]bool{"a": true, "c": true}
	m := ComputeRetrievalMetrics(ranked, relevant, 10)
	if m.RecallAt12 != 1 {
		t.Fatalf("Recall@12=%f want 1", m.RecallAt12)
	}
	if m.MRRAt10 != 0.5 {
		t.Fatalf("MRR@10=%f want 0.5", m.MRRAt10)
	}
	if m.NDCGAt10 <= 0 || m.NDCGAt10 > 1 {
		t.Fatalf("nDCG@10=%f out of range", m.NDCGAt10)
	}
}

func TestAnswerMetrics(t *testing.T) {
	relevant := map[string]bool{"doc-a": true, "doc-b": true}
	if got := ComputeExactMatchAt3([]string{"x", "doc-b", "y"}, relevant); got != 1 {
		t.Fatalf("ExactMatch@3=%f want 1", got)
	}
	if got := ComputeCitationPrecision([]string{"doc-a", "x", "doc-b", "y"}, relevant); got != 0.5 {
		t.Fatalf("CitationPrecision=%f want 0.5", got)
	}
	if got := ComputeUnsupportedClaimRate(4, 3); got != 0.25 {
		t.Fatalf("UnsupportedClaimRate=%f want 0.25", got)
	}
	if got := ComputeAbstentionPrecision(3, 3); got != 1 {
		t.Fatalf("AbstentionPrecision=%f want 1", got)
	}
}

func TestRerankDelta(t *testing.T) {
	relevant := map[string]bool{"a": true, "b": true}
	delta := ComputeRerankDelta([]string{"x", "a", "b"}, []string{"a", "b", "x"}, relevant, 10)
	if delta.NDCGAt10 <= 0 {
		t.Fatalf("rerank nDCG delta=%f want positive", delta.NDCGAt10)
	}
	if delta.RecallAt12 != 0 {
		t.Fatalf("rerank recall delta=%f want 0", delta.RecallAt12)
	}
}
