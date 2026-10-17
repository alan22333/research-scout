// Package planner implements the deterministic Stage 2 query planner.
//
// The planner is intentionally small and offline: routing decisions are
// derived from stable lexical signals, and every emitted subquery is bounded
// so a malformed or adversarial question cannot expand retrieval work.
package planner

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"research-scout/internal/domain"
)

const Version = "planner-stage2-v1"

const maxSubqueries = 5
const maxSubqueryBytes = 512

// Subquery is one bounded retrieval expression emitted by a plan.
type Subquery struct {
	Query   string  `json:"query"`
	Weight  float64 `json:"weight"`
	Purpose string  `json:"purpose"`
}

// QueryPlan is the deterministic routing and query-expansion decision persisted on
// a query turn.
type QueryPlan struct {
	Version    string             `json:"version"`
	Language   string             `json:"language"`
	Intent     domain.QueryIntent `json:"intent"`
	Route      string             `json:"route"`
	Question   string             `json:"question"`
	Subqueries []Subquery         `json:"subqueries"`
}

var doiPattern = regexp.MustCompile(`(?i)\b10\.\d{4,9}/[-._;()/:A-Z0-9]+\b`)

// Plan deterministically classifies a question and emits at most five
// weighted subqueries. It never calls an external provider.
func Plan(question string) QueryPlan {
	original := strings.TrimSpace(question)
	if original == "" {
		return QueryPlan{
			Version: Version, Language: "unknown", Intent: domain.IntentUnanswerable,
			Route: "hybrid_library", Question: original,
			Subqueries: []Subquery{{Query: "unanswerable empty question", Weight: 1, Purpose: "fallback"}},
		}
	}

	intent, route := classify(original)
	subqueries := expand(original, intent)
	if len(subqueries) == 0 {
		subqueries = []Subquery{{Query: bound(original), Weight: 1, Purpose: "fallback"}}
	}
	if len(subqueries) > maxSubqueries {
		subqueries = subqueries[:maxSubqueries]
	}
	return QueryPlan{
		Version: Version, Language: detectLanguage(original), Intent: intent, Route: route,
		Question: original, Subqueries: subqueries,
	}
}

func classify(question string) (domain.QueryIntent, string) {
	lower := strings.ToLower(question)
	if doiPattern.MatchString(question) || containsAny(lower, []string{"known item", "find this paper", "找一下这篇", "这篇论文的 doi", "标题为"}) {
		return domain.IntentKnownItem, "metadata_exact_then_hybrid"
	}
	if containsAny(lower, []string{" compare ", "comparison", "versus", " vs ", "difference", "比较", "对比", "区别", "差异"}) || strings.HasPrefix(lower, "compare ") {
		return domain.IntentMethodComparison, "hybrid_library_fulltext_first"
	}
	if containsAny(lower, []string{"recent", "latest", "new ", "last year", "last two years", "最近", "最新", "近两年", "近年来", "近一年"}) {
		return domain.IntentTemporal, "hybrid_library_with_time_filter"
	}
	if containsAny(lower, []string{"conflict", "contradict", "矛盾", "冲突", "不一致"}) {
		return domain.IntentConflictCheck, "hybrid_library"
	}
	return domain.IntentLibraryQA, "hybrid_library"
}

func expand(question string, intent domain.QueryIntent) []Subquery {
	// Keep the original question as the anchor. Additional subqueries are
	// deliberately deterministic and conservative; they only split explicit
	// comparison expressions or isolate a known DOI.
	switch intent {
	case domain.IntentKnownItem:
		out := make([]Subquery, 0, 2)
		if match := doiPattern.FindString(question); match != "" {
			out = append(out, Subquery{Query: bound(match), Weight: 1, Purpose: "exact_identifier"})
		}
		if len(out) == 0 || !strings.EqualFold(strings.TrimSpace(out[0].Query), question) {
			out = append(out, Subquery{Query: bound(question), Weight: 0.8, Purpose: "known_item_context"})
		}
		return out
	case domain.IntentMethodComparison:
		parts := splitComparison(question)
		if len(parts) > 1 {
			out := make([]Subquery, 0, len(parts)+1)
			for _, part := range parts {
				out = append(out, Subquery{Query: bound(part), Weight: 1, Purpose: "comparison_operand"})
			}
			out = append(out, Subquery{Query: bound(question), Weight: 0.8, Purpose: "comparison_context"})
			return out
		}
	case domain.IntentTemporal:
		// A time-filtered route still benefits from the original language;
		// retain one anchor and one recency-oriented expansion when separable.
		return []Subquery{
			{Query: bound(question), Weight: 1, Purpose: "temporal_anchor"},
			{Query: bound(question), Weight: 0.6, Purpose: "recency_filter"},
		}
	}
	return []Subquery{{Query: bound(question), Weight: 1, Purpose: "primary_question"}}
}

func splitComparison(question string) []string {
	normalized := strings.NewReplacer(
		" versus ", " vs ", " compared with ", " vs ", " compared to ", " vs ",
		" 与 ", " vs ", " 和 ", " vs ", " 对比 ", " vs ",
	).Replace(question)
	parts := strings.Split(normalized, " vs ")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func containsAny(s string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func detectLanguage(s string) string {
	for _, r := range s {
		if r >= '\u4e00' && r <= '\u9fff' {
			return "zh"
		}
	}
	return "en"
}

func bound(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxSubqueryBytes {
		return s
	}
	// Avoid cutting a UTF-8 rune in half.
	for len(s) > maxSubqueryBytes {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return strings.TrimSpace(s)
}
