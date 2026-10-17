package discovery

import "testing"

func TestGapRelativeToScope(t *testing.T) {
	t.Parallel()
	in := GapInput{
		NowYear: 2026,
		Seeds:   []LibraryDoc{{DocumentID: "seed", Title: "Differential privacy for federated learning", Year: 2021, InScope: true}},
		Library: []LibraryDoc{
			{DocumentID: "out-of-scope", DOI: "10.1/out", Title: "Differential privacy under shuffling", Year: 2022, InScope: false},
			{DocumentID: "in-scope", DOI: "10.1/in", Title: "Differential privacy in practice", Year: 2022, InScope: true},
		},
		Works: []ExternalWork{
			{DOI: "10.1/out", Title: "Differential privacy under shuffling", Year: 2022, Type: "article", CitationCount: 60},
			{DOI: "10.1/in", Title: "Differential privacy in practice", Year: 2022, Type: "article", CitationCount: 50},
		},
	}
	gaps := DetectGaps(in)
	if len(gaps) != 1 {
		t.Fatalf("gaps=%+v", gaps)
	}
	if gaps[0].Work.DOI != "10.1/out" {
		t.Fatalf("only the out-of-scope work is a gap: %+v", gaps[0])
	}
	if gaps[0].MissingRelativeTo != MissingCurrentScope {
		t.Fatalf("missing_relative_to=%s want current_scope", gaps[0].MissingRelativeTo)
	}
}

func TestGapExternalOnlyWhenLibraryUnknown(t *testing.T) {
	t.Parallel()
	in := GapInput{
		NowYear:        2026,
		LibraryUnknown: true,
		Seeds:          []LibraryDoc{{DocumentID: "seed", Title: "Differential privacy for federated learning", Year: 2021}},
		Works: []ExternalWork{
			{DOI: "10.1/method", Title: "Differential privacy under shuffling", Year: 2022, Type: "article", CitationCount: 60},
		},
	}
	gaps := DetectGaps(in)
	if len(gaps) != 1 || gaps[0].MissingRelativeTo != MissingExternalOnly {
		t.Fatalf("gaps=%+v", gaps)
	}
}

func TestCanonicalRequiresLibraryWideAbsence(t *testing.T) {
	t.Parallel()
	in := GapInput{
		NowYear: 2026,
		Seeds:   []LibraryDoc{{DocumentID: "seed", Title: "Differential privacy for federated learning", Year: 2021}},
		Library: []LibraryDoc{{DocumentID: "out", DOI: "10.1/survey", Title: "A survey of differential privacy", Year: 2019, InScope: false}},
		Works: []ExternalWork{
			{DOI: "10.1/survey", Title: "A survey of differential privacy", Year: 2019, Type: "review", CitationCount: 400},
		},
	}
	for _, g := range DetectGaps(in) {
		if g.Type == GapCanonical {
			t.Fatalf("canonical requires personal-library absence: %+v", g)
		}
	}
}

func TestGapExcludesWorkAlreadyInScope(t *testing.T) {
	t.Parallel()
	in := GapInput{
		NowYear: 2026,
		Library: []LibraryDoc{{DocumentID: "doc", DOI: "10.1/survey", Title: "A survey of differential privacy", Year: 2019, InScope: true}},
		Works: []ExternalWork{
			{DOI: "10.1/survey", Title: "A survey of differential privacy", Year: 2019, Type: "review", CitationCount: 400},
		},
	}
	if gaps := DetectGaps(in); len(gaps) != 0 {
		t.Fatalf("in-scope work must not be a gap: %+v", gaps)
	}
}
