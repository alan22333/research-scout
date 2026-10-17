// Package app wires the concrete Stage 3 providers into the query engine so the
// API and worker share one construction path. External providers are only
// constructed when discovery is explicitly enabled in config, which keeps the
// default (and CI) path fully offline.
package app

import (
	"time"

	"research-scout/internal/config"
	"research-scout/internal/discovery"
	"research-scout/internal/embedding"
	"research-scout/internal/query"
	"research-scout/internal/storage"
)

// BuildDiscovery constructs the external discovery service from config. It
// returns nil when discovery is disabled so the engine reports every external
// source as skipped without touching the network.
func BuildDiscovery(cfg config.Config) *discovery.Service {
	if !cfg.DiscoveryEnabled {
		return nil
	}
	timeout := cfg.DiscoveryTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	rps := cfg.S2RequestsPerSec
	if rps <= 0 {
		rps = 1
	}
	sources := []discovery.ScholarlyDiscoverySource{
		discovery.NewSemanticScholarSource(discovery.SemanticScholarConfig{
			APIKey: cfg.S2APIKey, RequestsPerSec: rps, Timeout: timeout,
		}),
		discovery.NewOpenAlexSource(discovery.OpenAlexConfig{
			Email: cfg.OpenAlexEmail, Timeout: timeout,
		}),
	}
	// Web search defaults to the explicit no-network provider. Even when
	// enabled it stays gated to landscape mode by the discovery service.
	var web discovery.WebSearchProvider = discovery.DisabledWebSearchProvider{}
	if cfg.WebSearchEnabled {
		web = discovery.NewQwenWebSearch(discovery.QwenWebSearchConfig{
			BaseURL: cfg.DashScopeBaseURL, APIKey: cfg.DashScopeAPIKey,
			Model: cfg.QwenSearchModel, Timeout: timeout,
		})
	}
	return &discovery.Service{
		Sources: sources,
		Web:     web,
		Config: discovery.ServiceConfig{
			SourceTimeout:    timeout,
			TotalTimeout:     timeout,
			DiscoveryEnabled: cfg.DiscoveryEnabled,
			WebSearchEnabled: cfg.WebSearchEnabled,
		},
	}
}

// NewEngine builds a query engine for the given store and config, wiring the
// external discovery service when it is enabled. The stub embedding keeps the
// deterministic local behavior used by the smoke loop and offline evaluation.
func NewEngine(store *storage.Store, cfg config.Config) *query.Engine {
	engine := &query.Engine{
		Store: store,
		Config: query.EngineConfig{
			PlannerVersion:         cfg.PlannerVersion,
			RetrievalConfigVersion: cfg.RetrievalConfigVersion,
			RerankVersion:          cfg.RerankVersion,
			PromptVersion:          cfg.PromptVersion,
		},
		Stub: embedding.NewStub(cfg),
	}
	if svc := BuildDiscovery(cfg); svc != nil {
		engine.Discovery = svc
	}
	return engine
}
