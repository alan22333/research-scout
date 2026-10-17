package eval

import "math"

// RetrievalMetrics contains the offline retrieval metrics used by the Stage 2
// quality gate. Values are ratios in [0,1].
type RetrievalMetrics struct {
	RecallAt5  float64 `json:"recall_at_5"`
	RecallAt12 float64 `json:"recall_at_12"`
	RecallAt40 float64 `json:"recall_at_40"`
	MRRAt10    float64 `json:"mrr_at_10"`
	NDCGAt10   float64 `json:"ndcg_at_10"`
	K          int     `json:"k"`
	Relevant   int     `json:"relevant"`
}

// ComputeRetrievalMetrics computes binary-relevance retrieval quality metrics
// from a ranked list of document identifiers.
func ComputeRetrievalMetrics(ranked []string, relevant map[string]bool, k int) RetrievalMetrics {
	if k <= 0 {
		k = 10
	}
	relevantCount := 0
	for _, ok := range relevant {
		if ok {
			relevantCount++
		}
	}
	m := RetrievalMetrics{K: k, Relevant: relevantCount}
	m.RecallAt5 = recall(ranked, relevant, 5)
	m.RecallAt12 = recall(ranked, relevant, 12)
	m.RecallAt40 = recall(ranked, relevant, 40)
	m.MRRAt10 = mrr(ranked, relevant, 10)
	m.NDCGAt10 = ndcg(ranked, relevant, 10)
	return m
}

func recall(ranked []string, relevant map[string]bool, limit int) float64 {
	if len(relevant) == 0 {
		return 0
	}
	hits := 0
	for i, id := range ranked {
		if limit > 0 && i >= limit {
			break
		}
		if relevant[id] {
			hits++
		}
	}
	return float64(hits) / float64(len(relevant))
}

func mrr(ranked []string, relevant map[string]bool, limit int) float64 {
	for i, id := range ranked {
		if i >= limit {
			break
		}
		if relevant[id] {
			return 1 / float64(i+1)
		}
	}
	return 0
}

func ndcg(ranked []string, relevant map[string]bool, limit int) float64 {
	n := limit
	if len(ranked) < n {
		n = len(ranked)
	}
	dcg := 0.0
	for i := 0; i < n; i++ {
		if relevant[ranked[i]] {
			dcg += 1 / math.Log2(float64(i+2))
		}
	}
	idealN := len(relevant)
	if idealN > limit {
		idealN = limit
	}
	idcg := 0.0
	for i := 0; i < idealN; i++ {
		idcg += 1 / math.Log2(float64(i+2))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// RerankDelta contains the change in ranking quality after reranking. Positive
// values mean the reranked order performed better than the pre-rerank order.
type RerankDelta struct {
	RecallAt5  float64 `json:"recall_at_5_delta"`
	RecallAt12 float64 `json:"recall_at_12_delta"`
	RecallAt40 float64 `json:"recall_at_40_delta"`
	MRRAt10    float64 `json:"mrr_at_10_delta"`
	NDCGAt10   float64 `json:"ndcg_at_10_delta"`
}

// ComputeExactMatchAt3 reports whether at least one relevant document appears
// in the first three ranked positions. It returns 1 or 0 so callers can average
// it across known-item questions.
func ComputeExactMatchAt3(ranked []string, relevant map[string]bool) float64 {
	for i, id := range ranked {
		if i >= 3 {
			break
		}
		if relevant[id] {
			return 1
		}
	}
	return 0
}

// ComputeCitationPrecision returns the fraction of cited documents that are
// annotated as relevant. An empty citation list has zero precision.
func ComputeCitationPrecision(citations []string, relevant map[string]bool) float64 {
	if len(citations) == 0 {
		return 0
	}
	hits := 0
	for _, id := range citations {
		if relevant[id] {
			hits++
		}
	}
	return float64(hits) / float64(len(citations))
}

// ComputeUnsupportedClaimRate returns unsupported claims divided by all
// claims. With no claims there is no unsupported claim to report.
func ComputeUnsupportedClaimRate(totalClaims, supportedClaims int) float64 {
	if totalClaims <= 0 {
		return 0
	}
	if supportedClaims < 0 {
		supportedClaims = 0
	}
	if supportedClaims > totalClaims {
		supportedClaims = totalClaims
	}
	return float64(totalClaims-supportedClaims) / float64(totalClaims)
}

// ComputeAbstentionPrecision returns correct abstentions divided by all
// abstentions. With no abstentions the system did not make an abstention claim,
// so zero is the safe deterministic value.
func ComputeAbstentionPrecision(abstentions, correctAbstentions int) float64 {
	if abstentions <= 0 {
		return 0
	}
	if correctAbstentions < 0 {
		correctAbstentions = 0
	}
	if correctAbstentions > abstentions {
		correctAbstentions = abstentions
	}
	return float64(correctAbstentions) / float64(abstentions)
}

// ComputeRerankDelta computes the difference between the post-rerank and
// pre-rerank retrieval metrics. The caller supplies the same relevance labels
// for both rankings.
func ComputeRerankDelta(before, after []string, relevant map[string]bool, k int) RerankDelta {
	beforeMetrics := ComputeRetrievalMetrics(before, relevant, k)
	afterMetrics := ComputeRetrievalMetrics(after, relevant, k)
	return RerankDelta{
		RecallAt5:  afterMetrics.RecallAt5 - beforeMetrics.RecallAt5,
		RecallAt12: afterMetrics.RecallAt12 - beforeMetrics.RecallAt12,
		RecallAt40: afterMetrics.RecallAt40 - beforeMetrics.RecallAt40,
		MRRAt10:    afterMetrics.MRRAt10 - beforeMetrics.MRRAt10,
		NDCGAt10:   afterMetrics.NDCGAt10 - beforeMetrics.NDCGAt10,
	}
}

// ComputeGapPrecision returns the fraction of reported gaps whose type is in
// the labeled expected set. An empty report or empty label set yields zero so
// the caller can decide whether the question is labeled. Stage 3 uses this to
// track how often the gap detector reports work the golden set actually marks
// as a real gap.
func ComputeGapPrecision(reported []string, expected map[string]bool) float64 {
	if len(reported) == 0 || len(expected) == 0 {
		return 0
	}
	hits := 0
	for _, gapType := range reported {
		if expected[gapType] {
			hits++
		}
	}
	return float64(hits) / float64(len(reported))
}
