package eval

import "testing"

func TestScoreQuestionComputesStage2Metrics(t *testing.T) {
	question := GoldenQuestion{
		ID:                    "q-metrics",
		Intent:                "known_item",
		RelevantDocumentIDs:   []string{"doc-a", "doc-b"},
		RelevantChunkIDs:      []string{"chunk-a", "chunk-b"},
		ExpectedEvidenceTypes: []string{"personal_fulltext"},
		MinEvidence:           1,
	}
	observation := questionObservation{
		CandidateChunks:   []string{"x", "chunk-a", "chunk-b"},
		SelectedChunks:    []string{"x", "chunk-a"},
		SelectedDocuments: []string{"x", "doc-b"},
		PreRerankChunks:   []string{"x", "chunk-b"},
		PostRerankChunks:  []string{"chunk-b", "x"},
		CitationDocuments: []string{"doc-a", "x"},
		CitationTypes:     []string{"personal_fulltext"},
		TotalClaims:       4,
		SupportedClaims:   3,
		Abstained:         false,
	}

	score := scoreQuestion(question, observation)
	if score.RecallAt40 != 1 {
		t.Fatalf("Recall@40=%f want 1", score.RecallAt40)
	}
	if score.RecallAt12 != 0.5 {
		t.Fatalf("Recall@12=%f want 0.5", score.RecallAt12)
	}
	if score.MRRAt10 != 0.5 {
		t.Fatalf("MRR@10=%f want 0.5", score.MRRAt10)
	}
	if score.ExactMatchAt3 != 1 {
		t.Fatalf("ExactMatch@3=%f want 1", score.ExactMatchAt3)
	}
	if score.CitationPrecision != 0.5 {
		t.Fatalf("CitationPrecision=%f want 0.5", score.CitationPrecision)
	}
	if score.UnsupportedClaimRate != 0.25 {
		t.Fatalf("UnsupportedClaimRate=%f want 0.25", score.UnsupportedClaimRate)
	}
	if score.RerankDelta.NDCGAt10 <= 0 {
		t.Fatalf("RerankDelta.nDCG@10=%f want positive", score.RerankDelta.NDCGAt10)
	}
}
