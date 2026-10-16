package query

import (
	"fmt"

	"research-scout/internal/domain"
)

func ValidateBriefClaims(b domain.Brief) error {
	for _, claim := range append(append([]domain.Claim{}, b.SummaryClaims...), b.Findings...) {
		if claim.VerificationStatus == domain.VerificationVerified && len(claim.Evidence) == 0 {
			return fmt.Errorf("verified claim %s has no claim_evidence", claim.ID)
		}
		for _, rel := range claim.Evidence {
			if rel.EvidenceID == "" || rel.EvidenceSpan == "" {
				return fmt.Errorf("claim %s has incomplete evidence relation", claim.ID)
			}
		}
	}
	return nil
}
