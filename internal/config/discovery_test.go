package config

import (
	"testing"
	"time"
)

func clearDiscoveryEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"DISCOVERY_ENABLED", "WEB_SEARCH_ENABLED", "DISCOVERY_TIMEOUT",
		"S2_API_KEY", "S2_REQUESTS_PER_SECOND", "OPENALEX_EMAIL",
		"DASHSCOPE_API_KEY", "QWEN_SEARCH_API_KEY", "DASHSCOPE_BASE_URL", "QWEN_SEARCH_MODEL",
	} {
		t.Setenv(key, "")
	}
}

// TestLoadDiscoveryDefaultsOff pins the frozen default: external discovery and
// provider-native web search must stay off (and therefore offline) unless an
// operator explicitly enables them.
func TestLoadDiscoveryDefaultsOff(t *testing.T) {
	clearDiscoveryEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DiscoveryEnabled || c.WebSearchEnabled {
		t.Fatalf("discovery/web search must default off: discovery=%v web=%v", c.DiscoveryEnabled, c.WebSearchEnabled)
	}
	if c.DiscoveryTimeout != 20*time.Second {
		t.Fatalf("DiscoveryTimeout=%s want 20s", c.DiscoveryTimeout)
	}
	if c.S2RequestsPerSec != 1 {
		t.Fatalf("S2RequestsPerSec=%d want 1", c.S2RequestsPerSec)
	}
	if c.QwenSearchModel != "qwen-plus" {
		t.Fatalf("QwenSearchModel=%q want qwen-plus", c.QwenSearchModel)
	}
	if c.DashScopeBaseURL == "" || c.ZoteroAPIBase == "" {
		t.Fatalf("provider base URLs must have defaults: %+v", c)
	}
}

// TestLoadDiscoverySecretsHaveNoDefault guards against a regression that bakes
// a real credential (or any non-empty placeholder) into the binary. Secrets
// must come only from the environment or a restricted local config file.
func TestLoadDiscoverySecretsHaveNoDefault(t *testing.T) {
	clearDiscoveryEnv(t)
	t.Setenv("SECRET_KEY", "local-dev-secret-change-me")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.S2APIKey != "" || c.DashScopeAPIKey != "" || c.OpenAlexEmail != "" {
		t.Fatalf("discovery secrets must default empty, got s2=%q dashscope=%q openalex=%q",
			c.S2APIKey, c.DashScopeAPIKey, c.OpenAlexEmail)
	}
}

// TestLoadDiscoveryEnvOverrides checks the documented environment names and the
// QWEN_SEARCH_API_KEY alias for the DashScope key.
func TestLoadDiscoveryEnvOverrides(t *testing.T) {
	clearDiscoveryEnv(t)
	t.Setenv("DISCOVERY_ENABLED", "true")
	t.Setenv("WEB_SEARCH_ENABLED", "1")
	t.Setenv("DISCOVERY_TIMEOUT", "5s")
	t.Setenv("S2_REQUESTS_PER_SECOND", "3")
	t.Setenv("S2_API_KEY", "s2-test-key")
	t.Setenv("OPENALEX_EMAIL", "researcher@example.org")
	t.Setenv("QWEN_SEARCH_API_KEY", "dashscope-via-alias")
	t.Setenv("QWEN_SEARCH_MODEL", "qwen-max")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.DiscoveryEnabled || !c.WebSearchEnabled {
		t.Fatalf("expected discovery and web search enabled: %+v", c)
	}
	if c.DiscoveryTimeout != 5*time.Second {
		t.Fatalf("DiscoveryTimeout=%s want 5s", c.DiscoveryTimeout)
	}
	if c.S2RequestsPerSec != 3 {
		t.Fatalf("S2RequestsPerSec=%d want 3", c.S2RequestsPerSec)
	}
	if c.S2APIKey != "s2-test-key" || c.DashScopeAPIKey != "dashscope-via-alias" {
		t.Fatalf("secret overrides not applied: %+v", c)
	}
	if c.OpenAlexEmail != "researcher@example.org" || c.QwenSearchModel != "qwen-max" {
		t.Fatalf("non-secret overrides not applied: %+v", c)
	}
}
