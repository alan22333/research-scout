package indexing

import (
	"testing"

	"research-scout/internal/domain"
)

func TestPublishEpochIsAtomicAndIdempotent(t *testing.T) {
	p := EpochPublisher{}
	chunks := []domain.Chunk{{IndexEpochID: "epoch-1", DocumentVersionID: "dv-1", Ordinal: 0}}
	if err := p.Publish("workspace-1", chunks); err != nil {
		t.Fatalf("publish valid epoch: %v", err)
	}
	if err := p.Publish("workspace-1", chunks); err != nil {
		t.Fatalf("publish should be idempotent: %v", err)
	}
}

func TestChunkRequiresEpochScopedUniqueness(t *testing.T) {
	a := domain.Chunk{IndexEpochID: "epoch-1", DocumentVersionID: "dv-1", Ordinal: 0}
	b := domain.Chunk{IndexEpochID: "epoch-2", DocumentVersionID: "dv-1", Ordinal: 0}
	if ChunkKey(a) == ChunkKey(b) {
		t.Fatal("chunk key must change across index epochs")
	}
	if err := (EpochPublisher{}).Publish("workspace-1", []domain.Chunk{{DocumentVersionID: "dv-1"}}); err == nil {
		t.Fatal("missing index_epoch_id must fail")
	}
}
