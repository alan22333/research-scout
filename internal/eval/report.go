package eval

import "time"

type Report struct {
	DatasetVersion         string         `json:"dataset_version"`
	RunID                  string         `json:"run_id"`
	RetrievalConfigVersion string         `json:"retrieval_config_version"`
	EmbeddingVersion       string         `json:"embedding_version"`
	IndexEpochID           string         `json:"index_epoch_id"`
	Metrics                map[string]any `json:"metrics"`
	PerIntent              map[string]any `json:"per_intent"`
	HardGateFailures       []string       `json:"hard_gate_failures"`
	SourceStatus           []string       `json:"source_status"`
	StartedAt              time.Time      `json:"started_at"`
	FinishedAt             time.Time      `json:"finished_at"`
}

func NewReport(datasetVersion, epochID string) Report {
	now := time.Now().UTC()
	return Report{
		DatasetVersion:         datasetVersion,
		RunID:                  "eval-stage0",
		RetrievalConfigVersion: "retrieval-v1",
		EmbeddingVersion:       "stage0-deterministic-embedding-v1",
		IndexEpochID:           epochID,
		Metrics:                map[string]any{},
		PerIntent:              map[string]any{},
		HardGateFailures:       []string{},
		SourceStatus:           []string{"library:available", "embedding:stub"},
		StartedAt:              now,
		FinishedAt:             now,
	}
}
