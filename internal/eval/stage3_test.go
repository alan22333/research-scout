package eval

import "testing"

func TestStage3Metrics(t *testing.T) {
	expected := map[string]bool{"canonical": true, "latest_progress": true}
	if got := ComputeGapPrecision([]string{"canonical", "related_method"}, expected); got != 0.5 {
		t.Fatalf("ComputeGapPrecision=%f want 0.5", got)
	}
	if got := ComputeGapPrecision(nil, expected); got != 0 {
		t.Fatalf("ComputeGapPrecision(empty)=%f want 0", got)
	}
	if got := ComputeGapPrecision([]string{"canonical"}, nil); got != 0 {
		t.Fatalf("ComputeGapPrecision(no labels)=%f want 0", got)
	}

	// The aggregate surfaces gap_precision only for labeled questions.
	var agg evalAggregate
	labeled := GoldenQuestion{ID: "q-gap", Intent: "gap_fill", Mode: "gap_fill", ExpectedGapTypes: []string{"canonical"}}
	agg.add(labeled, questionObservation{GapTypes: []string{"canonical", "related_method"}}, questionScore{GapPrecision: 0.5})
	unlabeled := GoldenQuestion{ID: "q-mylib", Intent: "library_qa", Mode: "my_library"}
	agg.add(unlabeled, questionObservation{}, questionScore{})
	if got := agg.metrics()["gap_precision"]; got != 0.5 {
		t.Fatalf("aggregate gap_precision=%v want 0.5", got)
	}
}

func TestScoreQuestionStage3Intents(t *testing.T) {
	// gap_fill / landscape questions label the gaps they expect to surface.
	gapQuestion := GoldenQuestion{
		ID:               "q-gap-fill",
		Intent:           "gap_fill",
		Mode:             "gap_fill",
		ExpectedGapTypes: []string{"related_method"},
		ExpectedEvidenceTypes: []string{
			"structured_abstract",
		},
		MinEvidence: 1,
	}
	gapObservation := questionObservation{
		GapTypes:        []string{"related_method", "latest_progress"},
		CitationTypes:   []string{"structured_abstract"},
		TotalClaims:     2,
		SupportedClaims: 1,
	}
	gapScore := scoreQuestion(gapQuestion, gapObservation)
	if gapScore.GapPrecision != 0.5 {
		t.Fatalf("gap_fill GapPrecision=%f want 0.5", gapScore.GapPrecision)
	}
	if gapScore.UnsupportedClaimRate != 0.5 {
		t.Fatalf("gap_fill UnsupportedClaimRate=%f want 0.5", gapScore.UnsupportedClaimRate)
	}
	if gapScore.AbstentionPrecision != 0 {
		t.Fatalf("gap_fill AbstentionPrecision=%f want 0 (answered question)", gapScore.AbstentionPrecision)
	}

	// temporal questions label recency gaps and must not be treated as abstentions.
	temporal := GoldenQuestion{
		ID: "q-temporal", Intent: "temporal", Mode: "landscape",
		ExpectedGapTypes: []string{"latest_progress"},
	}
	temporalScore := scoreQuestion(temporal, questionObservation{GapTypes: []string{"latest_progress"}})
	if temporalScore.GapPrecision != 1 {
		t.Fatalf("temporal GapPrecision=%f want 1", temporalScore.GapPrecision)
	}

	// conflict_check questions with no evidence must abstain correctly.
	conflict := GoldenQuestion{
		ID: "q-conflict", Intent: "conflict_check", Mode: "gap_fill",
		ExpectedGapTypes: []string{"canonical"}, Unanswerable: true,
	}
	conflictScore := scoreQuestion(conflict, questionObservation{GapTypes: nil, Abstained: true})
	if conflictScore.AbstentionPrecision != 1 {
		t.Fatalf("conflict AbstentionPrecision=%f want 1", conflictScore.AbstentionPrecision)
	}
	if conflictScore.GapPrecision != 0 {
		t.Fatalf("conflict GapPrecision=%f want 0 (no gaps reported)", conflictScore.GapPrecision)
	}
}

func TestEvalModeForDefaultsToMyLibrary(t *testing.T) {
	cases := map[string]string{
		"": "my_library", "my_library": "my_library", "gap_fill": "gap_fill",
		"landscape": "landscape", "bogus": "my_library", "  ": "my_library",
	}
	for in, want := range cases {
		if got := string(evalModeFor(GoldenQuestion{Mode: in})); got != want {
			t.Fatalf("evalModeFor(%q)=%q want %q", in, got, want)
		}
	}
}
