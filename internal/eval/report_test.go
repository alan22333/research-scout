package eval

import (
	"encoding/json"
	"testing"
)

func TestReportContainsVersionAndHardGateFields(t *testing.T) {
	r := NewReport("rag-golden-v1", "epoch-1")
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"dataset_version",
		"schema_version",
		"planner_version",
		"retrieval_config_version",
		"embedding_version",
		"index_epoch_id",
		"rerank_version",
		"prompt_version",
		"hard_gate_failures",
	} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("report missing %s", key)
		}
	}
}
