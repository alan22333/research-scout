package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"research-scout/internal/config"
	"research-scout/internal/discovery"
	"research-scout/internal/domain"
)

// discoverySmokeLimit is the hard per-provider request cap for the manual
// discovery smoke. Each provider executes exactly one search so a real-call
// check can never fan out into unbounded cost.
const discoverySmokeLimit = 5

// runDiscoverySmoke performs one bounded, read-only search per selected
// external provider and reports only status plus public counts/titles. It never
// prints credentials and never persists anything, so it is safe to run when a
// real, cost-sensitive provider check is required.
func runDiscoverySmoke(ctx context.Context, cfg config.Config, topic string, includeWeb bool, sources string) (map[string]any, error) {
	selected, explicitSources, err := parseSmokeSourceSelection(sources)
	if err != nil {
		return nil, err
	}
	if includeWeb {
		selected["web"] = true
	}
	if topic == "" {
		topic = "reciprocal rank fusion hybrid retrieval"
	}
	timeout := cfg.DiscoveryTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	rps := cfg.S2RequestsPerSec
	if rps <= 0 {
		rps = 1
	}

	out := map[string]any{}

	if selected["semantic_scholar"] {
		s2 := discovery.NewSemanticScholarSource(discovery.SemanticScholarConfig{
			APIKey: cfg.S2APIKey, RequestsPerSec: rps, Timeout: timeout, MaxPages: 1,
		})
		out["semantic_scholar"] = summarizeStructured(s2, ctx, topic, timeout)
	}
	if selected["openalex"] {
		oa := discovery.NewOpenAlexSource(discovery.OpenAlexConfig{Email: cfg.OpenAlexEmail, Timeout: timeout})
		out["openalex"] = summarizeStructured(oa, ctx, topic, timeout)
	}

	if selected["web"] {
		provider := discovery.NewQwenWebSearch(discovery.QwenWebSearchConfig{
			BaseURL: cfg.DashScopeBaseURL, APIKey: cfg.DashScopeAPIKey,
			Model: cfg.QwenSearchModel, Timeout: timeout,
		})
		out["qwen_web_search"] = summarizeWeb(provider, ctx, topic, timeout)
	} else if !explicitSources {
		out["qwen_web_search"] = map[string]any{
			"status": "skipped", "reason": "web_not_requested",
			"hint": "re-run with -web or -sources web to make one bounded web search request",
		}
	}
	out["topic_fingerprint"] = fingerprintTopic(topic)
	return out, nil
}

func parseSmokeSourceSelection(raw string) (map[string]bool, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "all") {
		return map[string]bool{"semantic_scholar": true, "openalex": true}, false, nil
	}
	selected := map[string]bool{}
	for _, item := range strings.Split(raw, ",") {
		switch strings.ToLower(strings.TrimSpace(item)) {
		case "s2", "semantic_scholar":
			selected["semantic_scholar"] = true
		case "openalex", "oa":
			selected["openalex"] = true
		case "web", "qwen", "qwen_web_search":
			selected["web"] = true
		case "":
			continue
		default:
			return nil, true, fmt.Errorf("unknown discovery-smoke source %q", item)
		}
	}
	if len(selected) == 0 {
		return nil, true, fmt.Errorf("discovery-smoke sources must not be empty")
	}
	return selected, true, nil
}

func summarizeStructured(src discovery.ScholarlyDiscoverySource, ctx context.Context, topic string, timeout time.Duration) map[string]any {
	qctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()
	resp, err := src.Search(qctx, discovery.SearchQuery{
		Topic: topic, Mode: domain.RetrievalModeGapFill,
		YearFrom: time.Now().Year() - 8, YearTo: time.Now().Year(),
		Limit: discoverySmokeLimit,
	})
	result := map[string]any{
		"status": string(resp.Status),
		"reason": resp.Reason,
		"works":  len(resp.Works),
	}
	if err != nil {
		result["error"] = err.Error()
		return result
	}
	titles := make([]string, 0, 3)
	for i, w := range resp.Works {
		if i >= 3 {
			break
		}
		titles = append(titles, fmt.Sprintf("%d|%.60s", w.Year, w.Title))
	}
	result["sample_titles"] = titles
	return result
}

func summarizeWeb(provider discovery.WebSearchProvider, ctx context.Context, topic string, timeout time.Duration) map[string]any {
	qctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()
	resp, err := provider.Search(qctx, discovery.WebSearchQuery{Topic: topic, Limit: 3})
	result := map[string]any{"status": string(resp.Status), "reason": resp.Reason, "contexts": len(resp.Contexts)}
	if err != nil {
		result["error"] = err.Error()
		return result
	}
	urls := make([]string, 0, 3)
	for i, c := range resp.Contexts {
		if i >= 3 {
			break
		}
		urls = append(urls, c.URL)
	}
	result["sample_urls"] = urls
	return result
}

// fingerprintTopic mirrors the audit-only topic fingerprint so smoke runs never
// log the full query text.
func fingerprintTopic(topic string) string {
	sum := 0
	for _, r := range topic {
		sum = (sum*31 + int(r)) & 0xffffffff
	}
	return fmt.Sprintf("t-%08x", sum)
}
