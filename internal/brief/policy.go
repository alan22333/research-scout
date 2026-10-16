package brief

import "research-scout/internal/domain"

func CanUseAsScientificEvidence(doc domain.Document) bool {
	if doc.RemovedAt != nil || doc.IndexStatus != domain.IndexIndexed || doc.EvidenceType == nil {
		return false
	}
	return *doc.EvidenceType == domain.EvidencePersonalFulltext || *doc.EvidenceType == domain.EvidenceStructuredAbstract
}
