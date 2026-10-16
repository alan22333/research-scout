package query

import (
	"testing"

	"research-scout/internal/domain"
)

func TestBuildBriefRequiresClaimEvidence(t *testing.T) {
	b := domain.Brief{Findings: []domain.Claim{{ID: "c1", VerificationStatus: domain.VerificationVerified}}}
	if err := ValidateBriefClaims(b); err == nil {
		t.Fatal("verified claim without evidence must fail")
	}

	b.Findings[0].Evidence = []domain.ClaimEvidence{{EvidenceID: "e1", EvidenceSpan: "supporting span"}}
	if err := ValidateBriefClaims(b); err != nil {
		t.Fatalf("claim with evidence should pass: %v", err)
	}
}
