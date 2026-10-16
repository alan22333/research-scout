package brief

import (
	"testing"

	"research-scout/internal/domain"
)

func TestMetadataOnlyCannotBecomeEvidence(t *testing.T) {
	doc := domain.Document{IndexStatus: domain.IndexMetadataOnly}
	if CanUseAsScientificEvidence(doc) {
		t.Fatal("metadata_only document must not become scientific evidence")
	}
}

func TestFulltextAndAbstractEvidenceAreAllowed(t *testing.T) {
	full := domain.EvidencePersonalFulltext
	abs := domain.EvidenceStructuredAbstract
	if !CanUseAsScientificEvidence(domain.Document{IndexStatus: domain.IndexIndexed, EvidenceType: &full}) {
		t.Fatal("fulltext evidence should be allowed")
	}
	if !CanUseAsScientificEvidence(domain.Document{IndexStatus: domain.IndexIndexed, EvidenceType: &abs}) {
		t.Fatal("abstract evidence should be allowed")
	}
}
