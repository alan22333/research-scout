package planner

import (
	"strings"
	"testing"

	"research-scout/internal/domain"
)

func TestPlannerRoutesAndLimits(t *testing.T) {
	tests := []struct {
		name       string
		question   string
		wantIntent domain.QueryIntent
		wantRoute  string
	}{
		{name: "known doi", question: "找一下 DOI 10.1145/12345.67890 的论文", wantIntent: domain.IntentKnownItem, wantRoute: "metadata_exact_then_hybrid"},
		{name: "method comparison", question: "比较 cache masking 和 constant-time 两种防御", wantIntent: domain.IntentMethodComparison, wantRoute: "hybrid_library_fulltext_first"},
		{name: "temporal", question: "最近两年有哪些新的 side-channel 缓解方法", wantIntent: domain.IntentTemporal, wantRoute: "hybrid_library_with_time_filter"},
		{name: "library qa", question: "这篇论文提出了什么方法", wantIntent: domain.IntentLibraryQA, wantRoute: "hybrid_library"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := Plan(tt.question)
			if plan.Intent != tt.wantIntent || plan.Route != tt.wantRoute {
				t.Fatalf("plan=%+v want intent=%s route=%s", plan, tt.wantIntent, tt.wantRoute)
			}
			if len(plan.Subqueries) == 0 || len(plan.Subqueries) > 5 {
				t.Fatalf("subquery count=%d want 1..5", len(plan.Subqueries))
			}
			for _, sub := range plan.Subqueries {
				if strings.TrimSpace(sub.Query) == "" || sub.Weight <= 0 || sub.Purpose == "" {
					t.Fatalf("invalid subquery: %+v", sub)
				}
			}
		})
	}
}

func TestPlannerPreservesOriginalAndFallsBack(t *testing.T) {
	const question = "  未知意图？  "
	plan := Plan(question)
	if plan.Route != "hybrid_library" {
		t.Fatalf("route=%q want hybrid_library", plan.Route)
	}
	if len(plan.Subqueries) != 1 {
		t.Fatalf("fallback subqueries=%d want 1", len(plan.Subqueries))
	}
	if plan.Subqueries[0].Query != strings.TrimSpace(question) {
		t.Fatalf("fallback query=%q want original trimmed question", plan.Subqueries[0].Query)
	}
}

func TestPlannerRejectsOrCapsUnboundedInput(t *testing.T) {
	question := strings.Repeat("alpha beta gamma delta epsilon zeta eta theta ", 80)
	plan := Plan(question)
	if len(plan.Subqueries) > 5 {
		t.Fatalf("subquery count=%d exceeds cap", len(plan.Subqueries))
	}
	for _, sub := range plan.Subqueries {
		if len(sub.Query) > 512 {
			t.Fatalf("subquery length=%d exceeds cap", len(sub.Query))
		}
	}
}
