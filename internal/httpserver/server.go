package httpserver

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"research-scout/internal/config"
	"research-scout/internal/domain"
	"research-scout/internal/identity"
	"research-scout/internal/query"
	"research-scout/internal/storage"
)

type Server struct {
	Config  config.Config
	Store   *storage.Store
	Engine  *query.Engine
	Adapter identity.Adapter
}

type apiError struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details"`
}

func NewRouter(cfg config.Config, store *storage.Store, engine *query.Engine, adapter identity.Adapter) *gin.Engine {
	s := &Server{Config: cfg, Store: store, Engine: engine, Adapter: adapter}
	r := gin.New()
	r.Use(gin.Recovery(), requestIDMiddleware(), s.errorMiddleware())
	r.GET("/healthz", s.health)
	r.GET("/readyz", s.ready)
	r.GET("/metrics", s.metrics)
	v1 := r.Group("/api/v1")
	v1.Use(s.identityMiddleware())
	{
		v1.GET("/me", s.me)
		v1.GET("/projects", s.listProjects)
		v1.POST("/projects", s.createProject)
		v1.GET("/projects/:projectId", s.getProject)
		v1.PATCH("/projects/:projectId", s.patchProject)
		v1.DELETE("/projects/:projectId", s.deleteProject)
		v1.GET("/projects/:projectId/scope", s.getScope)
		v1.PUT("/projects/:projectId/scope", s.putScope)
		v1.GET("/projects/:projectId/sessions", s.listSessions)
		v1.POST("/projects/:projectId/sessions", s.createSession)
		v1.GET("/sessions/:sessionId", s.getSession)
		v1.DELETE("/sessions/:sessionId", s.deleteSession)
		v1.POST("/sessions/:sessionId/cancel", s.cancelSession)
		v1.GET("/sessions/:sessionId/turns", s.listTurns)
		v1.POST("/sessions/:sessionId/turns", s.createTurn)
		v1.GET("/sessions/:sessionId/events", s.sessionEvents)
		v1.GET("/sessions/:sessionId/brief", s.getSessionBrief)
		v1.GET("/briefs/:briefId", s.getBrief)
		v1.GET("/briefs/:briefId/evidence", s.listBriefEvidence)
		v1.GET("/briefs/:briefId/gaps", s.listBriefGaps)
		v1.POST("/briefs/:briefId/feedback", s.createBriefFeedback)
		v1.GET("/briefs/:briefId/export", s.exportBrief)
		v1.GET("/evidence/:evidenceId", s.getEvidence)
		v1.GET("/discovery/search", s.searchDiscovery)
		v1.GET("/discovery/works/:externalWorkId", s.getDiscoveryWork)
		v1.GET("/discovery/works/:externalWorkId/citations", s.getDiscoveryCitations)
	}
	return r
}

func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if requestID == "" {
			requestID = storage.StableID("req", fmt.Sprint(time.Now().UnixNano()))
		}
		c.Set("request_id", requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

func (s *Server) errorMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if len(c.Errors) == 0 || c.Writer.Written() {
			return
		}
		err := c.Errors.Last().Err
		writeError(c, http.StatusInternalServerError, "internal", err.Error(), false)
	}
}

func (s *Server) identityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.Config.DevAuthEnabled || s.Adapter == nil {
			writeError(c, http.StatusUnauthorized, "unauthorized", "identity is not configured", false)
			c.Abort()
			return
		}
		id, err := s.Adapter.Resolve(c.Request)
		if err != nil {
			writeError(c, http.StatusUnauthorized, "unauthorized", err.Error(), false)
			c.Abort()
			return
		}
		c.Request = c.Request.WithContext(identity.WithContext(c.Request.Context(), id))
		c.Next()
	}
}

func (s *Server) requireIdentity(c *gin.Context) (identity.Identity, bool) {
	id, ok := identity.FromContext(c.Request.Context())
	if !ok {
		writeError(c, http.StatusUnauthorized, "unauthorized", "identity is missing", false)
		return identity.Identity{}, false
	}
	return id, true
}

func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Server) ready(c *gin.Context) {
	if s.Store == nil {
		writeError(c, http.StatusServiceUnavailable, "internal", "database is not configured", true)
		return
	}
	if err := s.Store.Ping(c.Request.Context()); err != nil {
		writeError(c, http.StatusServiceUnavailable, "internal", "database is not ready", true)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready", "database": "ready", "migrations": "ready"})
}

func (s *Server) metrics(c *gin.Context) {
	c.Data(http.StatusOK, "text/plain; version=0.0.4", []byte("# HELP research_scout_up Process readiness\n# TYPE research_scout_up gauge\nresearch_scout_up 1\n"))
}

func (s *Server) me(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"actor":             gin.H{"id": id.ActorID, "display_name": id.DisplayName},
		"workspace":         gin.H{"id": id.WorkspaceID, "name": "Local Workspace"},
		"role":              id.Role,
		"identity_provider": id.Provider,
	})
}

type projectCreateRequest struct {
	Name          string   `json:"name" binding:"required"`
	Description   string   `json:"description"`
	DefaultMode   string   `json:"default_mode"`
	ScopeMode     string   `json:"scope_mode"`
	CollectionIDs []string `json:"collection_ids"`
}

func (s *Server) createProject(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	var in projectCreateRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	p, err := s.Store.CreateProject(c.Request.Context(), storage.Project{
		WorkspaceID: id.WorkspaceID, Name: in.Name, Description: in.Description, DefaultMode: in.DefaultMode,
		ScopeMode: in.ScopeMode, CollectionIDs: in.CollectionIDs,
	})
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	c.JSON(http.StatusCreated, p)
}

func (s *Server) listProjects(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	items, err := s.Store.ListProjects(c.Request.Context(), id.WorkspaceID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal", err.Error(), true)
		return
	}
	if items == nil {
		items = []storage.Project{}
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) getProject(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	p, err := s.Store.GetProject(c.Request.Context(), id.WorkspaceID, c.Param("projectId"))
	if err != nil {
		writeError(c, http.StatusNotFound, "not_found", "project not found", false)
		return
	}
	c.JSON(http.StatusOK, p)
}

func (s *Server) patchProject(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	p, err := s.Store.GetProject(c.Request.Context(), id.WorkspaceID, c.Param("projectId"))
	if err != nil {
		writeError(c, http.StatusNotFound, "not_found", "project not found", false)
		return
	}
	var in struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		DefaultMode *string `json:"default_mode"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.Description != nil {
		p.Description = *in.Description
	}
	if in.DefaultMode != nil {
		p.DefaultMode = *in.DefaultMode
	}
	if p.Name == "" {
		writeError(c, http.StatusBadRequest, "invalid_request", "name is required", false)
		return
	}
	_, _ = s.Store.Pool().Exec(c.Request.Context(), `UPDATE projects SET name=$3,description=$4,default_mode=$5,updated_at=now() WHERE workspace_id=$1 AND id=$2 AND deleted_at IS NULL`, id.WorkspaceID, p.ID, p.Name, p.Description, p.DefaultMode)
	p, _ = s.Store.GetProject(c.Request.Context(), id.WorkspaceID, p.ID)
	c.JSON(http.StatusOK, p)
}

func (s *Server) getScope(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	p, err := s.Store.GetProject(c.Request.Context(), id.WorkspaceID, c.Param("projectId"))
	if err != nil {
		writeError(c, http.StatusNotFound, "not_found", "project not found", false)
		return
	}
	c.JSON(http.StatusOK, gin.H{"scope_mode": p.ScopeMode, "collection_ids": p.CollectionIDs, "version": p.ScopeVersion, "created_at": p.UpdatedAt})
}

func (s *Server) putScope(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	var in struct {
		ScopeMode     string   `json:"scope_mode" binding:"required"`
		CollectionIDs []string `json:"collection_ids"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	p, err := s.Store.UpdateProjectScope(c.Request.Context(), id.WorkspaceID, c.Param("projectId"), in.ScopeMode, in.CollectionIDs)
	if err != nil {
		code, status := mapScopeError(err)
		writeError(c, status, code, err.Error(), false)
		return
	}
	c.JSON(http.StatusOK, gin.H{"scope_mode": p.ScopeMode, "collection_ids": p.CollectionIDs, "version": p.ScopeVersion, "created_at": p.UpdatedAt})
}

type sessionCreateRequest struct {
	Question string `json:"question" binding:"required"`
	Mode     string `json:"mode" binding:"required"`
}

func (s *Server) createSession(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	var in sessionCreateRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	result, err := s.Engine.RunFirst(c.Request.Context(), id.WorkspaceID, c.Param("projectId"), in.Question, domain.RetrievalMode(in.Mode))
	if err != nil {
		code, status := mapEngineError(err)
		writeError(c, status, code, err.Error(), status >= 500)
		return
	}
	c.JSON(http.StatusAccepted, sessionResponse(result))
}

func (s *Server) listSessions(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	items, err := s.Store.ListSessions(c.Request.Context(), id.WorkspaceID, c.Param("projectId"))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal", err.Error(), true)
		return
	}
	if items == nil {
		items = []storage.Session{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "next_cursor": nil})
}

func (s *Server) getSession(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	sess, err := s.Store.GetSession(c.Request.Context(), id.WorkspaceID, c.Param("sessionId"))
	if err != nil {
		writeError(c, http.StatusNotFound, "not_found", "session not found", false)
		return
	}
	turns, _ := s.Store.ListTurns(c.Request.Context(), id.WorkspaceID, sess.ID)
	var currentTurnID string
	var mode = "my_library"
	if len(turns) > 0 {
		currentTurnID = turns[len(turns)-1].ID
		mode = turns[len(turns)-1].Mode
	}
	c.JSON(http.StatusOK, gin.H{
		"id": sess.ID, "project_id": sess.ProjectID, "current_turn_id": currentTurnID, "mode": mode,
		"status": sess.Status, "phase": sess.Phase, "events_url": "/api/v1/sessions/" + sess.ID + "/events",
		"source_status": query.DefaultSourceStatuses(),
		"created_at":    sess.CreatedAt,
	})
}

func (s *Server) listTurns(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	items, err := s.Store.ListTurns(c.Request.Context(), id.WorkspaceID, c.Param("sessionId"))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal", err.Error(), true)
		return
	}
	if items == nil {
		items = []storage.Turn{}
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) createTurn(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	var in sessionCreateRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	result, err := s.Engine.RunFollowUp(c.Request.Context(), id.WorkspaceID, c.Param("sessionId"), in.Question, domain.RetrievalMode(in.Mode))
	if err != nil {
		code, status := mapEngineError(err)
		writeError(c, status, code, err.Error(), status >= 500)
		return
	}
	c.JSON(http.StatusAccepted, result.Turn)
}

func (s *Server) getSessionBrief(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	brief, err := s.Engine.GetBrief(c.Request.Context(), id.WorkspaceID, c.Param("sessionId"))
	if err != nil {
		writeError(c, http.StatusNotFound, "not_found", "brief not found", false)
		return
	}
	c.JSON(http.StatusOK, brief)
}

func (s *Server) getBrief(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	brief, err := s.Engine.GetBriefByID(c.Request.Context(), id.WorkspaceID, c.Param("briefId"))
	if err != nil {
		writeError(c, http.StatusNotFound, "not_found", "brief not found", false)
		return
	}
	c.JSON(http.StatusOK, brief)
}

func (s *Server) listBriefEvidence(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	items, err := s.Engine.ListEvidence(c.Request.Context(), id.WorkspaceID, c.Param("briefId"))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal", err.Error(), true)
		return
	}
	if items == nil {
		items = []query.EvidenceView{}
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) listBriefGaps(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	briefID := c.Param("briefId")
	if _, err := s.Engine.GetBriefByID(c.Request.Context(), id.WorkspaceID, briefID); err != nil {
		writeError(c, http.StatusNotFound, "not_found", "brief not found", false)
		return
	}
	items, err := s.Engine.ListGaps(c.Request.Context(), id.WorkspaceID, briefID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal", err.Error(), true)
		return
	}
	if items == nil {
		items = []query.GapView{}
	}
	c.JSON(http.StatusOK, items)
}

// searchDiscovery exposes the structured external discovery read model. It is
// mode-gated and never calls web search.
func (s *Server) searchDiscovery(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	topic := strings.TrimSpace(c.Query("q"))
	if topic == "" {
		writeError(c, http.StatusBadRequest, "invalid_request", "q is required", false)
		return
	}
	mode := domain.RetrievalModeGapFill
	if raw := strings.TrimSpace(c.Query("mode")); raw != "" {
		mode = domain.RetrievalMode(raw)
	}
	page, err := s.Engine.SearchExternal(c.Request.Context(), id.WorkspaceID, topic, mode, c.QueryArray("sources"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	items := page.Items
	if items == nil {
		items = []query.ExternalWorkView{}
	}
	sourceStatus := page.SourceStatus
	if sourceStatus == nil {
		sourceStatus = []query.SourceStatus{}
	}
	meta := gin.H{"next_cursor": nil}
	if page.NextCursor != "" {
		meta["next_cursor"] = page.NextCursor
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "meta": meta, "source_status": sourceStatus})
}

// getDiscoveryWork returns one workspace-scoped external work.
func (s *Server) getDiscoveryWork(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	work, err := s.Engine.GetExternalWorkView(c.Request.Context(), id.WorkspaceID, c.Param("externalWorkId"))
	if err != nil {
		writeError(c, http.StatusNotFound, "not_found", "external work not found", false)
		return
	}
	c.JSON(http.StatusOK, work)
}

// getDiscoveryCitations returns the read-only citation/reference relations for
// one external work. It never expands into a second discovery hop.
func (s *Server) getDiscoveryCitations(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	items, err := s.Engine.ListCitedWorks(c.Request.Context(), id.WorkspaceID, c.Param("externalWorkId"))
	if err != nil {
		writeError(c, http.StatusNotFound, "not_found", "external work not found", false)
		return
	}
	if items == nil {
		items = []query.ExternalWorkView{}
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) deleteProject(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	if err := s.Store.DeleteProject(c.Request.Context(), id.WorkspaceID, c.Param("projectId")); err != nil {
		writeError(c, http.StatusNotFound, "not_found", "project not found", false)
		return
	}
	c.JSON(http.StatusAccepted, lifecycleTombstone(id.WorkspaceID, "project", c.Param("projectId")))
}

func (s *Server) deleteSession(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	if err := s.Store.DeleteSession(c.Request.Context(), id.WorkspaceID, c.Param("sessionId")); err != nil {
		writeError(c, http.StatusNotFound, "not_found", "session not found", false)
		return
	}
	c.JSON(http.StatusAccepted, lifecycleTombstone(id.WorkspaceID, "query_session", c.Param("sessionId")))
}

func (s *Server) cancelSession(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	sess, err := s.Store.CancelSession(c.Request.Context(), id.WorkspaceID, c.Param("sessionId"))
	if err != nil {
		writeError(c, http.StatusNotFound, "not_found", "session not found", false)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{
		"id": sess.ID, "project_id": sess.ProjectID, "status": sess.Status, "phase": sess.Phase,
		"events_url": "/api/v1/sessions/" + sess.ID + "/events",
		"created_at": sess.CreatedAt, "updated_at": sess.UpdatedAt,
	})
}

func (s *Server) createBriefFeedback(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	var in struct {
		Rating  *int   `json:"rating"`
		Comment string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	if in.Rating == nil || *in.Rating < -1 || *in.Rating > 1 {
		writeError(c, http.StatusBadRequest, "invalid_request", "rating must be -1, 0, or 1", false)
		return
	}
	if len([]rune(in.Comment)) > 2000 {
		writeError(c, http.StatusBadRequest, "invalid_request", "comment exceeds 2000 characters", false)
		return
	}
	if err := s.Store.AddBriefFeedback(c.Request.Context(), id.WorkspaceID, id.ActorID, c.Param("briefId"), *in.Rating, in.Comment); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(c, http.StatusNotFound, "not_found", "brief not found", false)
			return
		}
		writeError(c, http.StatusBadRequest, "invalid_request", err.Error(), false)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) exportBrief(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	brief, err := s.Engine.GetBriefByID(c.Request.Context(), id.WorkspaceID, c.Param("briefId"))
	if err != nil {
		writeError(c, http.StatusNotFound, "not_found", "brief not found", false)
		return
	}
	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "json")))
	switch format {
	case "json":
		c.JSON(http.StatusOK, brief)
	case "bibtex":
		c.Data(http.StatusOK, "application/x-bibtex; charset=utf-8", []byte(renderBibTeX(brief)))
	case "csv":
		c.Data(http.StatusOK, "text/csv; charset=utf-8", []byte(renderCSV(brief)))
	default:
		writeError(c, http.StatusBadRequest, "invalid_request", "unsupported export format", false)
	}
}

func lifecycleTombstone(workspaceID, objectType, objectID string) gin.H {
	purgeAfter := time.Now().UTC().Add(30 * 24 * time.Hour)
	return gin.H{
		"id":          storage.StableID("tombstone", workspaceID, objectType, objectID),
		"object_type": objectType, "object_id": objectID, "reason": "user_requested",
		"status": "active", "purge_after": purgeAfter,
	}
}

func renderBibTeX(brief query.BriefView) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, ev := range brief.Citations {
		if ev.Title == "" || seen[ev.Title] {
			continue
		}
		seen[ev.Title] = true
		key := "citation" + strconv.Itoa(len(seen))
		fmt.Fprintf(&b, "@misc{%s,\n  title = {%s},\n  note = {Research Scout brief %s},\n  howpublished = {\\url{/api/v1/evidence/%s}}\n}\n\n",
			key, ev.Title, brief.ID, ev.ID)
	}
	return b.String()
}

func renderCSV(brief query.BriefView) string {
	var b strings.Builder
	b.WriteString("title,evidence_type,score,evidence_id\n")
	seen := map[string]bool{}
	for _, ev := range brief.Citations {
		if ev.Title == "" || seen[ev.Title] {
			continue
		}
		seen[ev.Title] = true
		fmt.Fprintf(&b, "%q,%s,%.6f,%s\n", ev.Title, ev.EvidenceType, ev.Score, ev.ID)
	}
	return b.String()
}

func (s *Server) getEvidence(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	ev, err := s.Engine.GetEvidence(c.Request.Context(), id.WorkspaceID, c.Param("evidenceId"))
	if err != nil {
		writeError(c, http.StatusNotFound, "not_found", "evidence not found", false)
		return
	}
	c.JSON(http.StatusOK, ev)
}

func (s *Server) sessionEvents(c *gin.Context) {
	id, ok := s.requireIdentity(c)
	if !ok {
		return
	}
	if _, err := s.Store.GetSession(c.Request.Context(), id.WorkspaceID, c.Param("sessionId")); err != nil {
		writeError(c, http.StatusNotFound, "not_found", "session not found", false)
		return
	}
	after := int64(0)
	if raw := strings.TrimSpace(c.GetHeader("Last-Event-ID")); raw != "" {
		after, _ = strconv.ParseInt(raw, 10, 64)
	}
	if raw := c.Query("last_event_id"); raw != "" {
		after, _ = strconv.ParseInt(raw, 10, 64)
	}
	events, err := s.Engine.ListEventsAfter(c.Request.Context(), id.WorkspaceID, c.Param("sessionId"), after, 500)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "internal", err.Error(), true)
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	if len(events) == 0 {
		_, _ = c.Writer.WriteString(": connected\n\n")
		return
	}
	for _, ev := range events {
		data := storageMustJSON(ev.Data)
		_, _ = fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, data)
	}
}

func sessionResponse(result query.RunResult) gin.H {
	return gin.H{
		"id": result.Session.ID, "project_id": result.Session.ProjectID, "current_turn_id": result.Session.CurrentTurnID,
		"mode": result.Session.Mode, "status": result.Session.Status, "phase": result.Session.Phase,
		"events_url": result.Session.EventsURL, "source_status": result.Session.SourceStatus,
		"created_at": result.Session.CreatedAt,
	}
}

func mapEngineError(err error) (string, int) {
	if err == nil {
		return "", http.StatusOK
	}
	switch {
	case errors.Is(err, storage.ErrScopeEmpty):
		return "scope_empty", http.StatusUnprocessableEntity
	case errors.Is(err, storage.ErrForbidden):
		return "forbidden", http.StatusForbidden
	case errors.Is(err, storage.ErrConflict):
		return "conflict", http.StatusConflict
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "index_not_ready"):
		return "index_not_ready", http.StatusConflict
	case strings.Contains(msg, "required"), strings.Contains(msg, "invalid"):
		return "invalid_request", http.StatusBadRequest
	case strings.Contains(msg, "not found"), strings.Contains(msg, "no rows"):
		return "not_found", http.StatusNotFound
	default:
		return "internal", http.StatusInternalServerError
	}
}

// mapScopeError maps scope validation errors to stable HTTP codes. Validation
// failures must never be reported as a generic 400.
func mapScopeError(err error) (string, int) {
	switch {
	case errors.Is(err, storage.ErrScopeEmpty):
		return "scope_empty", http.StatusUnprocessableEntity
	case errors.Is(err, storage.ErrForbidden):
		return "forbidden", http.StatusForbidden
	case errors.Is(err, pgx.ErrNoRows):
		return "not_found", http.StatusNotFound
	default:
		return "invalid_request", http.StatusBadRequest
	}
}

func writeError(c *gin.Context, status int, code, message string, retryable bool) {
	if c.Writer.Written() {
		return
	}
	requestID, _ := c.Get("request_id")
	c.AbortWithStatusJSON(status, apiError{Error: errorBody{Code: code, Message: message, Retryable: retryable, RequestID: fmt.Sprint(requestID), Details: map[string]any{}}})
}

func storageMustJSON(v any) string {
	b, err := storage.JSON(v)
	if err != nil {
		return "{}"
	}
	return b
}
