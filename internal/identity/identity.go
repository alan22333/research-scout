package identity

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type Identity struct {
	ActorID     string
	WorkspaceID string
	DisplayName string
	Role        string
	Provider    string
}

type Adapter interface {
	Resolve(r *http.Request) (Identity, error)
}

type LocalAdapter struct {
	ActorID      string
	WorkspaceID  string
	AllowHeaders bool
}

func (a LocalAdapter) Resolve(r *http.Request) (Identity, error) {
	if a.ActorID == "" || a.WorkspaceID == "" {
		return Identity{}, fmt.Errorf("local identity is not configured")
	}
	actor := a.ActorID
	workspace := a.WorkspaceID
	if a.AllowHeaders {
		if v := strings.TrimSpace(r.Header.Get("X-Dev-Actor-ID")); v != "" {
			actor = v
		}
		if v := strings.TrimSpace(r.Header.Get("X-Dev-Workspace-ID")); v != "" {
			workspace = v
		}
	}
	return Identity{ActorID: actor, WorkspaceID: workspace, DisplayName: "Local User", Role: "user", Provider: "local"}, nil
}

type contextKey struct{}

func WithContext(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}
