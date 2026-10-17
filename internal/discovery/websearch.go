package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	if err := json.Unmarshal([]byte(cleanText(string(data))), &parsed); err != nil {
		return WebSearchResponse{Status: WebSearchUnavailable, Reason: string(ProviderInvalidResp)}, nil
	}
	now := time.Now().UTC()
	contexts := make([]WebContext, 0, len(parsed.SearchInfo.SearchResults))
	dropped := false
	for i, item := range parsed.SearchInfo.SearchResults {
		if i >= limit {
			break
		}
		link := strings.TrimSpace(item.URL)
		if link == "" {
			dropped = true
			continue
		}
		snippet := cleanText(item.Snippet)
		if snippet == "" {
			snippet = cleanText(item.Site)
		}
		contexts = append(contexts, WebContext{
			Title:       cleanText(item.Title),
			URL:         link,
			Snippet:     snippet,
			Provider:    "qwen",
			Model:       q.cfg.Model,
			ResponseID:  parsed.ID,
			RetrievedAt: now,
		})
	}
	status := WebSearchAvailable
	reason := ""
	if dropped {
		status = WebSearchDegraded
		reason = "invalid_response"
	}
	if len(contexts) == 0 {
		status = WebSearchUnavailable
		if reason == "" {
			reason = "web_search_no_sources"
		}
	}
	return WebSearchResponse{Contexts: contexts, Status: status, Reason: reason}, nil
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
