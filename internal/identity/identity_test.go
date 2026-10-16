package identity

import (
	"net/http"
	"testing"
)

func TestLocalAdapterDoesNotTrustHeadersByDefault(t *testing.T) {
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("X-Dev-Workspace-ID", "other")
	id, err := (LocalAdapter{ActorID: "actor", WorkspaceID: "workspace"}).Resolve(r)
	if err != nil {
		t.Fatal(err)
	}
	if id.WorkspaceID != "workspace" {
		t.Fatalf("header must be ignored by default, got %q", id.WorkspaceID)
	}
}

func TestLocalAdapterAllowsExplicitDevHeader(t *testing.T) {
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("X-Dev-Workspace-ID", "other")
	id, err := (LocalAdapter{ActorID: "actor", WorkspaceID: "workspace", AllowHeaders: true}).Resolve(r)
	if err != nil {
		t.Fatal(err)
	}
	if id.WorkspaceID != "other" {
		t.Fatalf("expected explicit dev header, got %q", id.WorkspaceID)
	}
}
