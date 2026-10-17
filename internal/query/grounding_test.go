package query

import (
	"strings"
	"testing"

	"research-scout/internal/domain"
)

func TestGroundingPolicy(t *testing.T) {
	fulltext := retrievedChunk{ChunkID: "c1", DocumentID: "d1", DocumentVersionID: "v1", Title: "Full text", Content: "A full-text result.", EvidenceType: domain.EvidencePersonalFulltext, Score: 0.9}
	abstract := retrievedChunk{ChunkID: "c2", DocumentID: "d2", DocumentVersionID: "v2", Title: "Abstract", Content: "An abstract-only result.", EvidenceType: domain.EvidenceStructuredAbstract, Score: 0.8}

	content := buildBriefContent("brief-1", "question", []retrievedChunk{fulltext, abstract})
	if len(content.SummaryClaims) != 1 || content.SummaryClaims[0].VerificationStatus != string(domain.VerificationVerified) {
		t.Fatalf("fulltext should allow verified summary: %+v", content.SummaryClaims)
	}
	if len(content.Findings) != 2 {
		t.Fatalf("findings=%d want 2", len(content.Findings))
	}
	if content.Findings[1].VerificationStatus != string(domain.VerificationPartial) {
		t.Fatalf("abstract finding status=%s want partial", content.Findings[1].VerificationStatus)
	}

	empty := buildBriefContent("brief-1", "question", nil)
	if !strings.Contains(strings.ToLower(empty.Summary), "insufficient") {
		t.Fatalf("empty summary=%q want insufficient", empty.Summary)
	}
	if len(empty.Findings) != 0 || len(empty.OpenQuestions) == 0 {
		t.Fatalf("empty content must not fabricate findings: %+v", empty)
	}
	if len(empty.SummaryClaims) != 0 {
		t.Fatalf("empty content must not fabricate summary claims: %+v", empty)
	}
}

// TestWebContextNeverVerified locks AC-016: web context is background only. It
// can never produce a finding and never makes a claim verified, whether or not
// it is mixed with scientific evidence.
func TestWebContextNeverVerified(t *testing.T) {
	web := retrievedChunk{ChunkID: "web-1", URL: "https://example.org/post", Title: "Blog", Content: "A blog post about the topic.", EvidenceType: domain.EvidenceWebContext, Score: 0.35}

	only := buildBriefContent("brief-web", "question", []retrievedChunk{web})
	if len(only.Findings) != 0 {
		t.Fatalf("web-only brief must not create findings: %+v", only.Findings)
	}
	if len(only.SummaryClaims) != 0 {
		t.Fatalf("web-only brief must not create summary claims: %+v", only.SummaryClaims)
	}
	foundBackground := false
	for _, q := range only.OpenQuestions {
		if strings.Contains(strings.ToLower(q), "background") {
			foundBackground = true
		}
	}
	if !foundBackground {
		t.Fatalf("web-only brief must explain background status: %+v", only.OpenQuestions)
	}

	fulltext := retrievedChunk{ChunkID: "c1", DocumentID: "d1", DocumentVersionID: "v1", Title: "Paper", Content: "A full-text scientific result.", EvidenceType: domain.EvidencePersonalFulltext, Score: 0.9}
	mixed := buildBriefContent("brief-mix", "question", []retrievedChunk{fulltext, web})
	if len(mixed.Findings) != 1 {
		t.Fatalf("mixed brief findings=%d want 1 (web excluded)", len(mixed.Findings))
	}
	if len(mixed.SummaryClaims) != 1 {
		t.Fatalf("mixed brief summary claims=%d want 1", len(mixed.SummaryClaims))
	}
	for _, rel := range mixed.SummaryClaims[0].ClaimEvidence {
		if rel.EvidenceID == evidenceIDFor("brief-mix", "web-1", 1) {
			t.Fatalf("web context must not back a summary claim: %+v", rel)
		}
	}
	for _, finding := range mixed.Findings {
		for _, rel := range finding.ClaimEvidence {
			if rel.EvidenceID == evidenceIDFor("brief-mix", "web-1", 1) {
				t.Fatalf("web context must not back a finding: %+v", finding)
			}
		}
	}
}

// TestConflictGrouping locks AC-015: two abstract-level statements in the same
// topic cluster that disagree on a negation marker must both be retained with
// opposite stances and surfaced as an open question.
func TestConflictGrouping(t *testing.T) {
	support := retrievedChunk{ChunkID: "c1", ExternalWorkID: "ext-1", Title: "Supports", Content: "Reranking improves dense retrieval recall on the benchmark.", EvidenceType: domain.EvidenceStructuredAbstract, Score: 0.6}
	refute := retrievedChunk{ChunkID: "c2", ExternalWorkID: "ext-2", Title: "Refutes", Content: "Reranking does not improve dense retrieval recall on the benchmark.", EvidenceType: domain.EvidenceStructuredAbstract, Score: 0.6}

	content := buildBriefContent("brief-conflict", "does reranking help", []retrievedChunk{support, refute})
	if len(content.Findings) != 2 {
		t.Fatalf("conflict findings=%d want 2", len(content.Findings))
	}
	stances := map[string]bool{}
	for _, finding := range content.Findings {
		for _, rel := range finding.ClaimEvidence {
			stances[rel.Stance] = true
		}
	}
	if !stances[string(domain.ClaimSupports)] || !stances[string(domain.ClaimRefutes)] {
		t.Fatalf("conflict must retain both stances, got %+v", stances)
	}
	joined := strings.Join(content.OpenQuestions, " ")
	if !strings.Contains(strings.ToLower(joined), "conflict") {
		t.Fatalf("conflict open question missing: %+v", content.OpenQuestions)
	}
	if content.SummaryClaims[0].VerificationStatus == string(domain.VerificationVerified) {
		t.Fatalf("abstract-level conflict must not be verified")
	}

	// Distinct topics that happen to share a negation must not be merged.
	unrelated := buildBriefContent("brief-ok", "q", []retrievedChunk{
		{ChunkID: "a", Title: "A", Content: "Graph neural networks improve node classification.", EvidenceType: domain.EvidenceStructuredAbstract, Score: 0.4},
		{ChunkID: "b", Title: "B", Content: "Differential privacy does not affect model accuracy.", EvidenceType: domain.EvidenceStructuredAbstract, Score: 0.4},
	})
	for _, q := range unrelated.OpenQuestions {
		if strings.Contains(strings.ToLower(q), "conflicting") {
			t.Fatalf("unrelated topics must not be flagged as conflicting: %+v", unrelated.OpenQuestions)
		}
	}
}

// TestModeSourceDispatch locks the frozen mode/source matrix at the query layer:
// my_library never reaches structured or web sources, gap_fill reaches only
// structured sources, and landscape may reach both.
func TestModeSourceDispatch(t *testing.T) {
	type expect struct {
		structured bool
		web        bool
		maxDocs    int
	}
	cases := map[domain.RetrievalMode]expect{
		domain.RetrievalModeMyLibrary: {structured: false, web: false, maxDocs: 6},
		domain.RetrievalModeGapFill:   {structured: true, web: false, maxDocs: 8},
		domain.RetrievalModeLandscape: {structured: true, web: true, maxDocs: 8},
	}
	for mode, want := range cases {
		if got := modeAllowsStructured(mode); got != want.structured {
			t.Fatalf("%s structured dispatch=%v want %v", mode, got, want.structured)
		}
		if got := modeAllowsWebSearch(mode); got != want.web {
			t.Fatalf("%s web dispatch=%v want %v", mode, got, want.web)
		}
		if got := maxDocumentsForMode(mode); got != want.maxDocs {
			t.Fatalf("%s maxDocuments=%d want %d", mode, got, want.maxDocs)
		}
	}
}
