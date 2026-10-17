package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// QwenWebSearchConfig configures the DashScope OpenAI-compatible web search adapter.
type QwenWebSearchConfig struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
	Client  *http.Client
}

// QwenWebSearch calls DashScope with enable_search=true and normalizes the
// provider-native search results into URL-backed web contexts.
type QwenWebSearch struct {
	cfg QwenWebSearchConfig
}

func NewQwenWebSearch(cfg QwenWebSearchConfig) *QwenWebSearch {
	if cfg.Model == "" {
		cfg.Model = "qwen-plus"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &QwenWebSearch{cfg: cfg}
}

func (q *QwenWebSearch) Name() string { return "qwen_web_search" }

type qwenSearchResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	SearchInfo struct {
		SearchResults []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
			Site    string `json:"site_name"`
		} `json:"search_results"`
	} `json:"search_info"`
}

var (
	qwenMarkdownLinkPattern = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	qwenRawURLPattern       = regexp.MustCompile(`https?://[^\s<>"'\]]+`)
)

func (q *QwenWebSearch) Search(ctx context.Context, in WebSearchQuery) (WebSearchResponse, error) {
	if strings.TrimSpace(q.cfg.APIKey) == "" {
		return WebSearchResponse{Status: WebSearchUnavailable, Reason: "web_search_unavailable"}, nil
	}
	topic := strings.TrimSpace(in.Topic)
	if topic == "" {
		return WebSearchResponse{Status: WebSearchDegraded, Reason: "empty_query"}, nil
	}
	limit := in.Limit
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	payload := map[string]any{
		"model": q.cfg.Model,
		"messages": []map[string]string{
			{"role": "user", "content": "Find authoritative background sources about: " + topic},
		},
		"enable_search": true,
		"search_options": map[string]any{
			"enable_source":   true,
			"search_strategy": "standard",
		},
	}
	if in.Freshness != "" {
		payload["search_options"].(map[string]any)["freshness"] = in.Freshness
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return WebSearchResponse{Status: WebSearchUnavailable, Reason: "invalid_request"}, err
	}
	endpoint := strings.TrimRight(q.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return WebSearchResponse{Status: WebSearchUnavailable, Reason: string(ProviderInvalidResp)}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+q.cfg.APIKey)

	resp, err := q.client().Do(req)
	if err != nil {
		return WebSearchResponse{Status: WebSearchUnavailable, Reason: string(ProviderTimeout)}, nil
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return WebSearchResponse{Status: WebSearchUnavailable, Reason: string(ProviderInvalidResp)}, nil
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return WebSearchResponse{Status: WebSearchUnavailable, Reason: "web_search_rate_limited"}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return WebSearchResponse{Status: WebSearchUnavailable, Reason: fmt.Sprintf("web_search_http_%d", resp.StatusCode)}, nil
	}
	var parsed qwenSearchResponse
	if err := json.Unmarshal(sanitizeJSONControlBytes(data), &parsed); err != nil {
		return WebSearchResponse{Status: WebSearchUnavailable, Reason: string(ProviderInvalidResp)}, nil
	}
	now := time.Now().UTC()

	contexts, dropped := qwenStructuredContexts(parsed, limit, now, q.cfg.Model)
	status := WebSearchAvailable
	reason := ""
	if len(contexts) == 0 {
		content := ""
		if len(parsed.Choices) > 0 {
			content = parsed.Choices[0].Message.Content
		}
		contexts = extractQwenContentContexts(content, limit, now, q.cfg.Model, parsed.ID)
		if len(contexts) > 0 {
			// DashScope may return search-backed URLs only inside the answer text.
			// Keep them as background context but expose the reduced trust level.
			status = WebSearchDegraded
			reason = "web_search_content_fallback"
		}
	}
	if len(contexts) == 0 {
		status = WebSearchUnavailable
		if reason == "" {
			reason = "web_search_no_sources"
		}
	} else if dropped {
		status = WebSearchDegraded
		reason = "invalid_response"
	}
	return WebSearchResponse{Contexts: contexts, Status: status, Reason: reason}, nil
}

func qwenStructuredContexts(parsed qwenSearchResponse, limit int, now time.Time, model string) ([]WebContext, bool) {
	contexts := make([]WebContext, 0, len(parsed.SearchInfo.SearchResults))
	dropped := false
	seen := map[string]struct{}{}
	for _, item := range parsed.SearchInfo.SearchResults {
		if len(contexts) >= limit {
			break
		}
		link, _, ok := normalizeQwenURL(item.URL)
		if !ok {
			dropped = true
			continue
		}
		if _, exists := seen[link]; exists {
			continue
		}
		seen[link] = struct{}{}
		snippet := cleanText(item.Snippet)
		if snippet == "" {
			snippet = cleanText(item.Site)
		}
		contexts = append(contexts, WebContext{
			Title:       cleanText(item.Title),
			URL:         link,
			Snippet:     snippet,
			Provider:    "qwen",
			Model:       model,
			ResponseID:  parsed.ID,
			RetrievedAt: now,
		})
	}
	return contexts, dropped
}

type qwenLineMatch struct {
	url   string
	title string
}

func extractQwenContentContexts(content string, limit int, now time.Time, model, responseID string) []WebContext {
	if strings.TrimSpace(content) == "" || limit <= 0 {
		return []WebContext{}
	}
	lines := strings.Split(content, "\n")
	contexts := make([]WebContext, 0, limit)
	seen := map[string]struct{}{}

	for lineIndex, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		matches := make([]qwenLineMatch, 0, 2)
		for _, match := range qwenMarkdownLinkPattern.FindAllStringSubmatch(line, -1) {
			if len(match) < 3 {
				continue
			}
			if link, _, ok := normalizeQwenURL(match[2]); ok {
				matches = append(matches, qwenLineMatch{url: link, title: cleanText(match[1])})
			}
		}
		for _, rawURL := range qwenRawURLPattern.FindAllString(line, -1) {
			if link, _, ok := normalizeQwenURL(rawURL); ok {
				matches = append(matches, qwenLineMatch{url: link})
			}
		}

		for _, match := range matches {
			if len(contexts) >= limit {
				return contexts
			}
			if _, exists := seen[match.url]; exists {
				continue
			}
			seen[match.url] = struct{}{}
			title := match.title
			if title == "" {
				title = qwenFallbackTitle(lines, lineIndex, match.url)
			}
			contexts = append(contexts, WebContext{
				Title:       title,
				URL:         match.url,
				Snippet:     qwenFallbackSnippet(lines, lineIndex, line, match.url),
				Provider:    "qwen",
				Model:       model,
				ResponseID:  responseID,
				RetrievedAt: now,
			})
		}
	}
	return contexts
}

func qwenFallbackTitle(lines []string, lineIndex int, rawURL string) string {
	_, host, _ := normalizeQwenURL(rawURL)
	for i := lineIndex - 1; i >= 0; i-- {
		line := cleanText(lines[i])
		if line == "" || containsHTTPURL(line) {
			continue
		}
		line = strings.TrimSpace(strings.Trim(line, "🔗•-–—:："))
		if line == "" {
			continue
		}
		return truncateRunes(line, 120)
	}
	return host
}

func qwenFallbackSnippet(lines []string, lineIndex int, line, rawURL string) string {
	withoutURL := cleanText(qwenRawURLPattern.ReplaceAllString(line, ""))
	withoutURL = strings.TrimSpace(strings.Trim(withoutURL, "🔗•-–—:："))
	if withoutURL != "" {
		return truncateRunes(withoutURL, 240)
	}
	for i := lineIndex - 1; i >= 0; i-- {
		previous := cleanText(lines[i])
		if previous == "" || containsHTTPURL(previous) {
			continue
		}
		previous = strings.TrimSpace(strings.Trim(previous, "🔗•-–—:："))
		if previous != "" {
			return truncateRunes(previous, 240)
		}
	}
	_, host, _ := normalizeQwenURL(rawURL)
	return host
}

func normalizeQwenURL(raw string) (string, string, bool) {
	link := strings.TrimSpace(raw)
	link = strings.TrimRight(link, ".,;:!?，。；：！？")
	link = strings.TrimRight(link, ")]}")
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host == "" {
		return "", "", false
	}
	return u.String(), host, true
}

func containsHTTPURL(s string) bool {
	return strings.Contains(strings.ToLower(s), "http://") || strings.Contains(strings.ToLower(s), "https://")
}

func truncateRunes(s string, max int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return strings.TrimSpace(string(runes[:max])) + "…"
}

func (q *QwenWebSearch) client() *http.Client {
	if q.cfg.Client != nil {
		return q.cfg.Client
	}
	return &http.Client{Timeout: q.cfg.Timeout}
}

// DisabledWebSearchProvider is the explicit no-network fallback.
type DisabledWebSearchProvider struct{}

func (DisabledWebSearchProvider) Name() string { return "disabled" }

func (DisabledWebSearchProvider) Search(context.Context, WebSearchQuery) (WebSearchResponse, error) {
	return WebSearchResponse{Status: WebSearchSkipped, Reason: "web_search_disabled"}, nil
}
