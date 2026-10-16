package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"research-scout/internal/config"
	"research-scout/internal/embedding"
	"research-scout/internal/httpserver"
	"research-scout/internal/identity"
	"research-scout/internal/query"
	"research-scout/internal/storage"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	if cfg.AutoMigrate {
		migrationsDir := os.Getenv("RS_MIGRATIONS_DIR")
		if migrationsDir == "" {
			migrationsDir = "migrations"
		}
		if _, err := store.Migrate(ctx, migrationsDir); err != nil {
			return err
		}
	}
	if cfg.DevAuthEnabled {
		if err := storage.SeedLocal(ctx, store, cfg.DevActorID, cfg.DevWorkspaceID); err != nil {
			return err
		}
	}
	adapter := identity.LocalAdapter{ActorID: cfg.DevActorID, WorkspaceID: cfg.DevWorkspaceID, AllowHeaders: cfg.DevAllowActorHeader && (cfg.AppEnv == "local" || cfg.AppEnv == "test")}
	engine := &query.Engine{Store: store, Config: query.EngineConfig{PlannerVersion: cfg.PlannerVersion, RetrievalConfigVersion: cfg.RetrievalConfigVersion, PromptVersion: cfg.PromptVersion}, Stub: embedding.NewStub(cfg)}
	router := httpserver.NewRouter(cfg, store, engine, adapter)
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("research-scout api listening on %s", cfg.HTTPAddr)
		errCh <- server.ListenAndServe()
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-stop:
		log.Printf("received %s, shutting down", sig)
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
