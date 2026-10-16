package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"research-scout/internal/config"
	"research-scout/internal/credentials"
	"research-scout/internal/storage"
	"research-scout/internal/zotero"
)

// runSyncZotero performs a full or incremental Zotero Web API sync for the
// configured local workspace. The API key is never echoed to stdout/stderr.
func runSyncZotero(ctx context.Context, store *storage.Store, cfg config.Config, flags syncFlags) (map[string]any, error) {
	apiKey, err := loadAPIKey(flags.apiKeyFile)
	if err != nil {
		return nil, err
	}
	libraryID := flags.libraryID
	if strings.TrimSpace(libraryID) == "" {
		libraryID = strings.TrimSpace(os.Getenv("ZOTERO_LIBRARY_ID"))
	}
	credentialStore, err := credentials.NewStore(store, cfg.SecretKey)
	if err != nil {
		return nil, err
	}
	client := zotero.NewHTTPClient(cfg.ZoteroRequestConcurrency)
	source := zotero.NewWebAPISource(cfg.ZoteroAPIBase, cfg.ZoteroAPIVersion, client)
	syncer := zotero.Syncer{
		Store: store, Source: source, Credentials: credentialStore,
		Config: zotero.SyncConfig{
			EmbeddingVersion:       cfg.EmbeddingModel + ":" + cfg.EmbeddingNormalization,
			EmbeddingDim:           cfg.EmbeddingDim,
			RetrievalConfigVersion: cfg.RetrievalConfigVersion,
		},
	}
	cred := zotero.Credential{APIKey: apiKey, LibraryType: "user", LibraryID: libraryID}
	// With no explicit library id, validate first so the account id is used.
	if cred.LibraryID == "" {
		info, err := source.Validate(ctx, cred)
		if err != nil {
			return nil, err
		}
		if info.LibraryID != "" {
			cred.LibraryID = info.LibraryID
		} else {
			cred.LibraryID = info.UserID
		}
	}
	mode := zotero.SyncMode(strings.TrimSpace(flags.mode))
	if mode == "" {
		mode = zotero.SyncModeFull
	}
	result, err := syncer.Sync(ctx, cfg.DevWorkspaceID, cfg.DevActorID, cred, mode)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"status": "ok", "mode": string(mode), "workspace_id": cfg.DevWorkspaceID,
		"result": result,
	}, nil
}

// runSyncStatus prints a redacted view of the connection, cursor, coverage and
// most recent sync runs. It never returns credential material.
func runSyncStatus(ctx context.Context, store *storage.Store, cfg config.Config, libraryID string) (map[string]any, error) {
	if strings.TrimSpace(libraryID) == "" {
		libraryID = strings.TrimSpace(os.Getenv("ZOTERO_LIBRARY_ID"))
	}
	query := `
		SELECT id, library_type, library_id,
		       (encrypted_credential IS NOT NULL AND encrypted_credential <> '') AS has_credential,
		       status, last_library_version, last_sync_at, last_success_at, coverage_jsonb
		FROM zotero_connections
		WHERE workspace_id=$1`
	args := []any{cfg.DevWorkspaceID}
	if libraryID != "" {
		query += ` AND library_id=$2`
		args = append(args, libraryID)
	}
	query += ` ORDER BY updated_at DESC`

	rows, err := store.Pool().Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var conns []map[string]any
	connectionIDs := make([]string, 0)
	for rows.Next() {
		var id, libraryType, libID, status string
		var hasCred bool
		var lastVersion int64
		var lastSync, lastSuccess *time.Time
		var coverage []byte
		if err := rows.Scan(&id, &libraryType, &libID, &hasCred, &status, &lastVersion, &lastSync, &lastSuccess, &coverage); err != nil {
			return nil, err
		}
		conns = append(conns, map[string]any{
			"connection_id": id, "library_type": libraryType, "library_id": libID,
			"has_credential": hasCred, "status": status, "last_library_version": lastVersion,
			"last_sync_at": lastSync, "last_success_at": lastSuccess, "coverage": jsonRaw(coverage),
		})
		connectionIDs = append(connectionIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var runs []map[string]any
	if len(connectionIDs) > 0 {
		runRows, err := store.Pool().Query(ctx, `
			SELECT id, connection_id, mode, status, from_version, to_version, counters_jsonb, error_code, started_at, finished_at
			FROM zotero_sync_runs
			WHERE workspace_id=$1 AND connection_id = ANY($2)
			ORDER BY started_at DESC LIMIT 10
		`, cfg.DevWorkspaceID, connectionIDs)
		if err != nil {
			return nil, err
		}
		defer runRows.Close()
		for runRows.Next() {
			var id, connectionID, mode, status string
			var fromVersion int64
			var toVersion *int64
			var counters []byte
			var errCode *string
			var started time.Time
			var finished *time.Time
			if err := runRows.Scan(&id, &connectionID, &mode, &status, &fromVersion, &toVersion, &counters, &errCode, &started, &finished); err != nil {
				return nil, err
			}
			runs = append(runs, map[string]any{
				"run_id": id, "connection_id": connectionID, "mode": mode, "status": status,
				"from_version": fromVersion, "to_version": toVersion, "counters": jsonRaw(counters),
				"error_code": errCode, "started_at": started, "finished_at": finished,
			})
		}
		if err := runRows.Err(); err != nil {
			return nil, err
		}
	}

	coverage, err := store.Coverage(ctx, cfg.DevWorkspaceID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"status": "ok", "workspace_id": cfg.DevWorkspaceID,
		"connections": conns, "recent_sync_runs": runs, "document_coverage": coverage,
	}, nil
}

func runPurgeLifecycle(ctx context.Context, store *storage.Store, cfg config.Config) (map[string]any, error) {
	purged, err := zotero.PurgeDue(ctx, store, cfg.DevWorkspaceID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "purged": purged, "workspace_id": cfg.DevWorkspaceID}, nil
}

// loadAPIKey resolves the Zotero API key from a permission-restricted file or
// the ZOTERO_API_KEY environment variable. It refuses world/group-readable
// files so private keys are not sourced from shared locations.
func loadAPIKey(apiKeyFile string) (string, error) {
	if strings.TrimSpace(apiKeyFile) != "" {
		info, err := os.Stat(apiKeyFile)
		if err != nil {
			return "", fmt.Errorf("read api key file: %w", err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("api key file %s must be chmod 600 (or stricter)", apiKeyFile)
		}
		raw, err := os.ReadFile(apiKeyFile)
		if err != nil {
			return "", fmt.Errorf("read api key file: %w", err)
		}
		key := parseAPIKey(raw)
		if key == "" {
			return "", errors.New("api key file is empty")
		}
		return key, nil
	}
	key := strings.TrimSpace(os.Getenv("ZOTERO_API_KEY"))
	if key == "" {
		return "", errors.New("ZOTERO_API_KEY is not set; pass --api-key-file or export ZOTERO_API_KEY")
	}
	return key, nil
}

// parseAPIKey accepts either a bare key or a KEY=VALUE env-style file.
func parseAPIKey(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.IndexByte(line, '='); idx >= 0 {
			name := strings.TrimSpace(line[:idx])
			if name == "ZOTERO_API_KEY" || name == "ZOTERO_KEY" {
				return strings.Trim(strings.TrimSpace(line[idx+1:]), `"'`)
			}
			continue
		}
		return line
	}
	return ""
}

func jsonRaw(b []byte) any {
	if len(strings.TrimSpace(string(b))) == 0 {
		return map[string]any{}
	}
	return string(b)
}

// syncFlags holds CLI options shared by the sync commands.
type syncFlags struct {
	mode       string
	libraryID  string
	apiKeyFile string
}
