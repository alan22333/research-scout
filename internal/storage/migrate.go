package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

type MigrationResult struct {
	Applied []string `json:"applied"`
	Skipped []string `json:"skipped"`
}

func (s *Store) Migrate(ctx context.Context, migrationsDir string) (MigrationResult, error) {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("read migrations directory: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		return MigrationResult{}, fmt.Errorf("no migrations found in %s", migrationsDir)
	}

	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return MigrationResult{}, fmt.Errorf("ensure schema_migrations: %w", err)
	}

	result := MigrationResult{}
	for _, name := range names {
		version := strings.TrimSuffix(name, filepath.Ext(name))
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&exists); err != nil {
			return result, err
		}
		if exists {
			result.Skipped = append(result.Skipped, version)
			continue
		}
		sqlBytes, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			return result, fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return result, err
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return result, fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return result, fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return result, fmt.Errorf("commit migration %s: %w", name, err)
		}
		result.Applied = append(result.Applied, version)
	}
	return result, nil
}
