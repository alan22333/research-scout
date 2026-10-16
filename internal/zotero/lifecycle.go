package zotero

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"research-scout/internal/storage"
)

// PurgeDue removes derived content for document tombstones whose purge deadline
// has passed. It deletes chunks, document versions, collection links and any
// brief evidence that referenced the removed document while preserving the
// minimal tombstone audit row. The operation is idempotent.
func PurgeDue(ctx context.Context, store *storage.Store, workspaceID string, now time.Time) (int, error) {
	if store == nil {
		return 0, fmt.Errorf("store is required")
	}
	tx, err := store.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id, object_id FROM lifecycle_tombstones
		WHERE workspace_id=$1 AND object_type='document' AND status='active' AND purge_after <= $2
		FOR UPDATE
	`, workspaceID, now)
	if err != nil {
		return 0, err
	}
	type tombstone struct{ id, documentID string }
	var due []tombstone
	for rows.Next() {
		var t tombstone
		if err := rows.Scan(&t.id, &t.documentID); err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(due) == 0 {
		return 0, nil
	}

	purged := 0
	for _, t := range due {
		// Remove derived references first, then documents' derived rows.
		if _, err := tx.Exec(ctx, `
			DELETE FROM claim_evidence
			WHERE workspace_id=$1 AND evidence_id IN (
				SELECT id FROM evidence WHERE workspace_id=$1 AND document_id=$2
			)
		`, workspaceID, t.documentID); err != nil {
			return 0, fmt.Errorf("purge claim_evidence: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM evidence WHERE workspace_id=$1 AND document_id=$2`, workspaceID, t.documentID); err != nil {
			return 0, fmt.Errorf("purge evidence: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM chunks WHERE workspace_id=$1 AND document_id=$2`, workspaceID, t.documentID); err != nil {
			return 0, fmt.Errorf("purge chunks: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM document_collections WHERE workspace_id=$1 AND document_id=$2`, workspaceID, t.documentID); err != nil {
			return 0, fmt.Errorf("purge document_collections: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE documents SET active_version_id=NULL WHERE id=$1 AND workspace_id=$2`, t.documentID, workspaceID); err != nil {
			return 0, fmt.Errorf("clear active version: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM index_epoch_documents WHERE workspace_id=$1 AND document_id=$2`, workspaceID, t.documentID); err != nil {
			return 0, fmt.Errorf("purge index_epoch_documents: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM document_versions WHERE workspace_id=$1 AND document_id=$2`, workspaceID, t.documentID); err != nil {
			return 0, fmt.Errorf("purge document_versions: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE lifecycle_tombstones SET status='purged', completed_at=$2
			WHERE id=$1 AND workspace_id=$3
		`, t.id, now, workspaceID); err != nil {
			return 0, fmt.Errorf("mark tombstone purged: %w", err)
		}
		purged++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return purged, nil
}
