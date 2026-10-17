package query

import (
	"testing"

	"research-scout/internal/retrieval"
)

func TestSourceStatusesRecordRerankFallback(t *testing.T) {
	statuses := sourceStatusesForRerank(retrieval.RerankOutcome{DegradedReason: "rerank_provider_unavailable"})
	found := false
	for _, status := range statuses {
		if status.Source != "rerank" {
			continue
		}
		found = true
		if status.Status != "degraded" || status.DegradedReason == nil || *status.DegradedReason != "rerank_provider_unavailable" {
			t.Fatalf("rerank status=%+v want degraded provider-unavailable", status)
		}
	}
	if !found {
		t.Fatal("rerank source status missing")
	}
}

func TestSourceStatusesRecordRerankUse(t *testing.T) {
	statuses := sourceStatusesForRerank(retrieval.RerankOutcome{Used: true, Provider: "reranker", Version: "test-v1"})
	for _, status := range statuses {
		if status.Source == "rerank" {
			if status.Status != "available" || status.DegradedReason != nil {
				t.Fatalf("rerank status=%+v want available", status)
			}
			return
		}
	}
	t.Fatal("rerank source status missing")
}
