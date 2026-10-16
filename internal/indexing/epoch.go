package indexing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"research-scout/internal/domain"
)

type EpochPublisher struct{}

func (EpochPublisher) Publish(workspaceID string, chunks []domain.Chunk) error {
	if workspaceID == "" {
		return fmt.Errorf("workspace_id is required")
	}
	for _, c := range chunks {
		if c.IndexEpochID == "" || c.DocumentVersionID == "" {
			return fmt.Errorf("chunk identity must include index_epoch_id and document_version_id")
		}
	}
	return nil
}

func ChunkKey(c domain.Chunk) string {
	sum := sha256.Sum256([]byte(c.IndexEpochID + "\x00" + c.DocumentVersionID + "\x00" + fmt.Sprint(c.Ordinal)))
	return hex.EncodeToString(sum[:])
}
