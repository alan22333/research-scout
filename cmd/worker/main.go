package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"research-scout/internal/app"
	"research-scout/internal/config"
	"research-scout/internal/domain"
	"research-scout/internal/embedding"
	"research-scout/internal/eval"
	"research-scout/internal/query"
	"research-scout/internal/storage"
	"research-scout/internal/zotero"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: worker <migrate|seed|import-fixture|smoke-stage0|discovery-smoke|eval-rag|sync-zotero|sync-status|purge-lifecycle> [flags]")
	}
	command := os.Args[1]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	migrationsDir := fs.String("migrations", env("RS_MIGRATIONS_DIR", "migrations"), "migration directory")
	fixturePath := fs.String("fixture", env("RS_ZOTERO_FIXTURE", "scripts/fixtures/zotero-stage0.yaml"), "fixture path")
	datasetPath := fs.String("dataset", "", "golden dataset path")
	kind := fs.String("kind", "all", "eval kind: all, retrieval, brief")
	syncMode := fs.String("mode", "full", "sync mode: full or incremental")
	libraryID := fs.String("library-id", env("ZOTERO_LIBRARY_ID", ""), "Zotero user/library id (optional)")
	apiKeyFile := fs.String("api-key-file", env("ZOTERO_API_KEY_FILE", ""), "path to a chmod 600 file holding the Zotero API key")
	smokeTopic := fs.String("topic", "reciprocal rank fusion hybrid retrieval", "topic for one bounded discovery-smoke search")
	smokeWeb := fs.Bool("web", false, "also make one bounded provider-native web search request")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	timeout := 2 * time.Minute
	switch command {
	case "sync-zotero", "purge-lifecycle":
		// Zotero free-tier rate limits force serialized, backed-off requests.
		timeout = 45 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// discovery-smoke is a read-only, cost-bounded provider check: it needs no
	// database and never persists results, so it runs before the store opens.
	if command == "discovery-smoke" {
		return printJSON(runDiscoverySmoke(ctx, cfg, *smokeTopic, *smokeWeb), nil)
	}

	store, err := storage.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	switch command {
	case "migrate":
		result, err := store.Migrate(ctx, *migrationsDir)
		return printJSON(result, err)
	case "seed":
		if _, err := store.Migrate(ctx, *migrationsDir); err != nil {
			return err
		}
		if err := storage.SeedLocal(ctx, store, cfg.DevActorID, cfg.DevWorkspaceID); err != nil {
			return err
		}
		return printJSON(map[string]any{"status": "seeded", "actor_id": cfg.DevActorID, "workspace_id": cfg.DevWorkspaceID}, nil)
	case "import-fixture":
		if _, err := store.Migrate(ctx, *migrationsDir); err != nil {
			return err
		}
		if err := storage.SeedLocal(ctx, store, cfg.DevActorID, cfg.DevWorkspaceID); err != nil {
			return err
		}
		result, err := importFixture(ctx, store, cfg, *fixturePath)
		return printJSON(result, err)
	case "smoke-stage0":
		result, err := smoke(ctx, store, cfg, *migrationsDir, *fixturePath)
		return printJSON(result, err)
	case "eval-rag":
		if _, err := smoke(ctx, store, cfg, *migrationsDir, *fixturePath); err != nil {
			return err
		}
		if *datasetPath == "" {
			*datasetPath = cfg.RAGEvalDataset
		}
		engine := app.NewEngine(store, cfg)
		result, err := eval.Run(ctx, store, engine, cfg, *datasetPath, *kind)
		if err != nil {
			return err
		}
		if len(result.Report.HardGateFailures) > 0 {
			return fmt.Errorf("RAG hard gates failed: %s", strings.Join(result.Report.HardGateFailures, "; "))
		}
		return printJSON(map[string]any{"report_path": result.ReportPath, "report": result.Report}, nil)
	case "sync-zotero":
		if _, err := store.Migrate(ctx, *migrationsDir); err != nil {
			return err
		}
		if err := storage.SeedLocal(ctx, store, cfg.DevActorID, cfg.DevWorkspaceID); err != nil {
			return err
		}
		result, err := runSyncZotero(ctx, store, cfg, syncFlags{mode: *syncMode, libraryID: *libraryID, apiKeyFile: *apiKeyFile})
		return printJSON(result, err)
	case "sync-status":
		if _, err := store.Migrate(ctx, *migrationsDir); err != nil {
			return err
		}
		result, err := runSyncStatus(ctx, store, cfg, *libraryID)
		return printJSON(result, err)
	case "purge-lifecycle":
		if _, err := store.Migrate(ctx, *migrationsDir); err != nil {
			return err
		}
		result, err := runPurgeLifecycle(ctx, store, cfg)
		return printJSON(result, err)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func smoke(ctx context.Context, store *storage.Store, cfg config.Config, migrationsDir, fixturePath string) (map[string]any, error) {
	if _, err := store.Migrate(ctx, migrationsDir); err != nil {
		return nil, err
	}
	if err := storage.SeedLocal(ctx, store, cfg.DevActorID, cfg.DevWorkspaceID); err != nil {
		return nil, err
	}
	imported, err := importFixture(ctx, store, cfg, fixturePath)
	if err != nil {
		return nil, err
	}
	projectID := storage.StableID("project", cfg.DevWorkspaceID, "stage0-smoke")
	if _, err := store.CreateProject(ctx, storage.Project{
		ID: projectID, WorkspaceID: cfg.DevWorkspaceID, Name: "Stage 0 Smoke",
		Description: "Local fixture-backed smoke project", DefaultMode: "my_library", ScopeMode: "all_library",
	}); err != nil {
		return nil, err
	}
	engine := app.NewEngine(store, cfg)
	const question = "hybrid retrieval reciprocal rank fusion"
	sessionID := storage.StableID("session", cfg.DevWorkspaceID, projectID, "stage0-smoke", question)
	result, err := reusableSmokeResult(ctx, store, engine, cfg, projectID, sessionID, imported.EpochID, question)
	if err != nil {
		return nil, err
	}
	brief := result.Brief
	if len(brief.Citations) == 0 {
		return nil, fmt.Errorf("smoke query returned no evidence")
	}
	if strings.Contains(strings.ToLower(brief.Summary), "unindexed") {
		return nil, fmt.Errorf("metadata-only content entered summary")
	}
	claims, err := countBriefClaims(ctx, store, cfg.DevWorkspaceID, brief.ID)
	if err != nil {
		return nil, err
	}
	if claims == 0 {
		return nil, fmt.Errorf("brief did not create claims")
	}
	traceable, err := countTraceableClaims(ctx, store, cfg.DevWorkspaceID, brief.ID)
	if err != nil {
		return nil, err
	}
	if traceable != claims {
		return nil, fmt.Errorf("claim traceability incomplete: %d/%d", traceable, claims)
	}
	var metadataEvidence int
	if err := store.Pool().QueryRow(ctx, `
		SELECT count(*) FROM evidence e JOIN documents d ON d.id=e.document_id
		WHERE e.workspace_id=$1 AND e.brief_id=$2 AND (d.index_status='metadata_only' OR d.evidence_type IS NULL)
	`, cfg.DevWorkspaceID, brief.ID).Scan(&metadataEvidence); err != nil {
		return nil, err
	}
	if metadataEvidence != 0 {
		return nil, fmt.Errorf("metadata-only document entered evidence")
	}
	active, err := store.ActiveEpochID(ctx, cfg.DevWorkspaceID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"status": "ok", "workspace_id": cfg.DevWorkspaceID, "index_epoch_id": active, "import": imported,
		"session_id": result.Session.ID, "turn_id": result.Turn.ID, "brief_id": brief.ID,
		"citations": len(brief.Citations), "claims": claims, "traceable_claims": traceable,
		"metadata_only_evidence": metadataEvidence,
	}, nil
}

func reusableSmokeResult(ctx context.Context, store *storage.Store, engine *query.Engine, cfg config.Config, projectID, sessionID, epochID, question string) (query.RunResult, error) {
	sess, err := store.GetSession(ctx, cfg.DevWorkspaceID, sessionID)
	if err == nil {
		if brief, briefErr := engine.GetBrief(ctx, cfg.DevWorkspaceID, sessionID); briefErr == nil {
			return query.RunResult{
				Session: query.SessionView{Session: sess, CurrentTurnID: brief.TurnID, Mode: string(domain.RetrievalModeMyLibrary), EventsURL: "/api/v1/sessions/" + sess.ID + "/events"},
				Turn:    storage.Turn{ID: brief.TurnID, SessionID: sess.ID, IndexEpochID: brief.IndexEpochID},
				Brief:   brief,
			}, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return query.RunResult{}, err
	}

	if sess.ID == "" {
		sess, err = store.CreateSession(ctx, storage.CreateSessionInput{
			ID: sessionID, WorkspaceID: cfg.DevWorkspaceID, ProjectID: projectID, Title: "Stage 0 Smoke", Mode: string(domain.RetrievalModeMyLibrary), EpochID: epochID,
		})
		if err != nil {
			return query.RunResult{}, err
		}
	}
	return engine.RunFollowUp(ctx, cfg.DevWorkspaceID, sess.ID, question, domain.RetrievalModeMyLibrary)
}

func importFixture(ctx context.Context, store *storage.Store, cfg config.Config, fixturePath string) (zotero.ImportResult, error) {
	fixture, err := zotero.LoadFixture(fixturePath)
	if err != nil {
		return zotero.ImportResult{}, err
	}
	importer := zotero.Importer{Store: store, EmbeddingVersion: embedding.Stage0Version, EmbeddingDim: cfg.EmbeddingDim}
	return importer.Import(ctx, cfg.DevWorkspaceID, cfg.DevActorID, fixture)
}

func countBriefClaims(ctx context.Context, store *storage.Store, workspaceID, briefID string) (int, error) {
	var n int
	err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM claims WHERE workspace_id=$1 AND brief_id=$2`, workspaceID, briefID).Scan(&n)
	return n, err
}

func countTraceableClaims(ctx context.Context, store *storage.Store, workspaceID, briefID string) (int, error) {
	var n int
	err := store.Pool().QueryRow(ctx, `
		SELECT count(DISTINCT c.id)
		FROM claims c
		JOIN claim_evidence ce ON ce.claim_id=c.id
		JOIN evidence e ON e.id=ce.evidence_id
		JOIN document_versions dv ON dv.id=e.document_version_id
		JOIN chunks ch ON ch.id=e.chunk_id
		WHERE c.workspace_id=$1 AND c.brief_id=$2
	`, workspaceID, briefID).Scan(&n)
	return n, err
}

func printJSON(v any, err error) error {
	if err != nil {
		return err
	}
	b, marshalErr := json.MarshalIndent(v, "", "  ")
	if marshalErr != nil {
		return marshalErr
	}
	fmt.Println(string(b))
	return nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
