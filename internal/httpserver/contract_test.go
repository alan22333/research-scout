package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"

	"research-scout/internal/config"
)

type openAPIDocument struct {
	Paths map[string]map[string]any `yaml:"paths"`
}

func TestContractImplementedRoutesExistInOpenAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	document := loadOpenAPIDocument(t)
	router := NewRouter(config.Config{}, nil, nil, nil)

	implemented := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/healthz"},
		{http.MethodGet, "/readyz"},
		{http.MethodGet, "/metrics"},
		{http.MethodGet, "/api/v1/me"},
		{http.MethodGet, "/api/v1/projects"},
		{http.MethodPost, "/api/v1/projects"},
		{http.MethodGet, "/api/v1/projects/:projectId"},
		{http.MethodPatch, "/api/v1/projects/:projectId"},
		{http.MethodGet, "/api/v1/projects/:projectId/scope"},
		{http.MethodPut, "/api/v1/projects/:projectId/scope"},
		{http.MethodGet, "/api/v1/projects/:projectId/sessions"},
		{http.MethodPost, "/api/v1/projects/:projectId/sessions"},
		{http.MethodGet, "/api/v1/sessions/:sessionId"},
		{http.MethodGet, "/api/v1/sessions/:sessionId/turns"},
		{http.MethodPost, "/api/v1/sessions/:sessionId/turns"},
		{http.MethodGet, "/api/v1/sessions/:sessionId/events"},
		{http.MethodGet, "/api/v1/sessions/:sessionId/brief"},
		{http.MethodGet, "/api/v1/briefs/:briefId"},
		{http.MethodGet, "/api/v1/briefs/:briefId/evidence"},
		{http.MethodGet, "/api/v1/briefs/:briefId/gaps"},
		{http.MethodGet, "/api/v1/evidence/:evidenceId"},
	}

	registered := map[string]bool{}
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, route := range implemented {
		if !registered[route.method+" "+route.path] {
			t.Fatalf("implemented route is not registered: %s %s", route.method, route.path)
		}
		openAPIPath := strings.TrimPrefix(route.path, "/api/v1")
		if openAPIPath == route.path {
			continue
		}
		openAPIPath = normalizeRouteParams(openAPIPath)
		methods, ok := document.Paths[openAPIPath]
		if !ok {
			t.Fatalf("route missing from OpenAPI: %s %s (%s)", route.method, route.path, openAPIPath)
		}
		if _, ok := methods[strings.ToLower(route.method)]; !ok {
			t.Fatalf("method missing from OpenAPI: %s %s", route.method, openAPIPath)
		}
	}
}

func TestContractHealthAndUnauthorizedShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(config.Config{}, nil, nil, nil)

	health := httptest.NewRecorder()
	router.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", health.Code)
	}

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("me status = %d, want 401", unauthorized.Code)
	}
	var payload struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(unauthorized.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if payload.Error.Code != "unauthorized" || payload.Error.RequestID == "" {
		t.Fatalf("unexpected error payload: %+v", payload.Error)
	}
}

func loadOpenAPIDocument(t *testing.T) openAPIDocument {
	t.Helper()
	path := filepath.Join("..", "..", "openapi", "research-scout.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read OpenAPI contract: %v", err)
	}
	var document openAPIDocument
	if err := yaml.Unmarshal(b, &document); err != nil {
		t.Fatalf("parse OpenAPI contract: %v", err)
	}
	if len(document.Paths) == 0 {
		t.Fatal("OpenAPI contract has no paths")
	}
	return document
}

func normalizeRouteParams(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, ":") {
			parts[i] = "{" + strings.TrimPrefix(part, ":") + "}"
		}
	}
	return strings.Join(parts, "/")
}
