package httpserver

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"research-scout/internal/config"
)

func TestContractStage2Routes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(config.Config{}, nil, nil, nil)
	want := []string{
		"DELETE /api/v1/projects/:projectId",
		"DELETE /api/v1/sessions/:sessionId",
		"POST /api/v1/sessions/:sessionId/cancel",
		"POST /api/v1/briefs/:briefId/feedback",
		"GET /api/v1/briefs/:briefId/export",
	}
	registered := map[string]bool{}
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, route := range want {
		if !registered[route] {
			t.Fatalf("missing Stage 2 route: %s", route)
		}
	}
	if http.MethodDelete != "DELETE" {
		t.Fatal("unreachable")
	}
}
