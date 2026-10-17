package httpserver

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"research-scout/internal/config"
)

// TestContractDiscoveryRoutes pins the Stage 3 external discovery read surface:
// the routes must be registered and present in the OpenAPI contract with the
// same path parameters the router uses.
func TestContractDiscoveryRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(config.Config{}, nil, nil, nil)
	document := loadOpenAPIDocument(t)

	want := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/discovery/search"},
		{http.MethodGet, "/api/v1/discovery/works/:externalWorkId"},
		{http.MethodGet, "/api/v1/discovery/works/:externalWorkId/citations"},
	}

	registered := map[string]bool{}
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, route := range want {
		if !registered[route.method+" "+route.path] {
			t.Fatalf("missing Stage 3 discovery route: %s %s", route.method, route.path)
		}
		openAPIPath := normalizeRouteParams(strings.TrimPrefix(route.path, "/api/v1"))
		methods, ok := document.Paths[openAPIPath]
		if !ok {
			t.Fatalf("discovery route missing from OpenAPI: %s %s (%s)", route.method, route.path, openAPIPath)
		}
		if _, ok := methods[strings.ToLower(route.method)]; !ok {
			t.Fatalf("discovery method missing from OpenAPI: %s %s", route.method, openAPIPath)
		}
	}
}
