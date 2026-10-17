package discovery

import "testing"

func TestNormalizeDOIEquivalentForms(t *testing.T) {
	t.Parallel()
	cases := []string{
		"10.1145/12345",
		"  10.1145/12345 ",
		"https://doi.org/10.1145/12345",
		"http://dx.doi.org/10.1145/12345",
		"doi:10.1145/12345",
		"DOI: 10.1145/12345",
	}
	for _, c := range cases {
		if got := NormalizeDOI(c); got != "10.1145/12345" {
			t.Fatalf("NormalizeDOI(%q)=%q", c, got)
		}
	}
}

func TestIdentityKeyPriorityAndStability(t *testing.T) {
	t.Parallel()
	doiA := ExternalWork{DOI: "https://doi.org/10.1/AbC", S2ID: "S1", OpenAlexID: "W1", Title: "Alpha", Year: 2020}
	doiB := ExternalWork{DOI: "10.1/abc", S2ID: "S2", OpenAlexID: "W2", Title: "Different title", Year: 2021}
	if Key(doiA) != Key(doiB) {
		t.Fatalf("same DOI must share identity key: %q vs %q", Key(doiA), Key(doiB))
	}
	s2A := ExternalWork{S2ID: "S9", OpenAlexID: "W1", Title: "Beta", Year: 2019}
	s2B := ExternalWork{S2ID: "S9", OpenAlexID: "W2", Title: "Beta", Year: 2019}
	if Key(s2A) != Key(s2B) {
		t.Fatalf("same S2 id must share identity key")
	}
	noID := ExternalWork{Title: "No identifiers", Year: 2018}
	if Key(noID) == "" {
		t.Fatalf("title fingerprint + year must still produce a key")
	}
}

func TestTitleFingerprintRequiresYearToMerge(t *testing.T) {
	t.Parallel()
	fp := FingerprintTitle("  Hybrid   Retrieval: A Survey!  ")
	if fp != "hybrid retrieval a survey" {
		t.Fatalf("fingerprint=%q", fp)
	}
	left := ExternalWork{Title: "Hybrid Retrieval", Year: 2020}
	right := ExternalWork{Title: "hybrid   retrieval", Year: 0}
	if Key(left) == Key(right) {
		t.Fatalf("title-only candidates must not merge when year is missing")
	}
	right.Year = 2020
	if Key(left) != Key(right) {
		t.Fatalf("same title and year must merge")
	}
}

func TestMergeSuggestAndLibraryDedup(t *testing.T) {
	t.Parallel()
	thin := ExternalWork{DOI: "10.1/x", Title: "Work", Year: 2020, Source: SourceSemanticScholar}
	fat := ExternalWork{DOI: "10.1/x", Title: "Work", Year: 2020, Abstract: "abstract", Venue: "Venue", CitationCount: 42, Source: SourceOpenAlex}
	merged := Merge(thin, fat)
	if merged.Abstract != "abstract" || merged.Venue != "Venue" || merged.CitationCount != 42 {
		t.Fatalf("merged=%+v", merged)
	}

	lib := []LibraryDoc{
		{DocumentID: "doc-1", DOI: "10.1/in-library", Title: "In Library Work", Year: 2019},
		{DocumentID: "doc-2", DOI: "10.1/in-scope", Title: "Scoped Work", Year: 2021, InScope: true},
	}
	out := DedupAgainstLibrary([]ExternalWork{
		{DOI: "10.1/in-library", Title: "In Library Work", Year: 2019},
		{DOI: "10.1/in-scope", Title: "Scoped Work", Year: 2021},
		{DOI: "10.1/outside", Title: "Outside Work", Year: 2022},
	}, lib)
	if len(out) != 3 {
		t.Fatalf("len=%d", len(out))
	}
	if !out[0].AlreadyInLibrary || out[0].LibraryDocumentID != "doc-1" {
		t.Fatalf("out[0]=%+v", out[0])
	}
	if !out[1].AlreadyInLibrary || out[1].LibraryDocumentID != "doc-2" || !out[1].InCurrentScope {
		t.Fatalf("out[1]=%+v", out[1])
	}
	if out[2].AlreadyInLibrary {
		t.Fatalf("outside work must not be marked in library")
	}
}

func TestDetectGapsCanonicalRelatedAndLatest(t *testing.T) {
	t.Parallel()
	in := GapInput{
		NowYear: 2026,
		Seeds:   []LibraryDoc{{DocumentID: "seed-1", Title: "Differential privacy for federated learning", Year: 2021}},
		Library: []LibraryDoc{{DocumentID: "known-1", DOI: "10.1/known", Title: "Known work", Year: 2020, InScope: true}},
		Works: []ExternalWork{
			{DOI: "10.1/survey", Title: "A survey of differential privacy", Year: 2019, Type: "review", CitationCount: 400},
			{DOI: "10.1/method", Title: "Differential privacy under shuffling", Year: 2022, Type: "article", CitationCount: 60},
			{DOI: "10.1/new", Title: "Differential privacy advances", Year: 2025, Type: "article", CitationCount: 5},
			{DOI: "10.1/known", Title: "Known work", Year: 2020, Type: "article", CitationCount: 90},
			{DOI: "10.1/offtopic", Title: "Unrelated algae bloom study", Year: 2024, Type: "article", CitationCount: 3},
		},
	}
	gaps := DetectGaps(in)
	byType := map[GapType][]Gap{}
	for _, g := range gaps {
		byType[g.Type] = append(byType[g.Type], g)
	}
	if len(byType[GapCanonical]) != 1 || byType[GapCanonical][0].Work.DOI != "10.1/survey" {
		t.Fatalf("canonical=%+v", byType[GapCanonical])
	}
	if len(byType[GapRelatedMethod]) != 1 || byType[GapRelatedMethod][0].Work.DOI != "10.1/method" {
		t.Fatalf("related=%+v", byType[GapRelatedMethod])
	}
	if len(byType[GapLatestProgress]) != 1 || byType[GapLatestProgress][0].Work.DOI != "10.1/new" {
		t.Fatalf("latest=%+v", byType[GapLatestProgress])
	}
	for _, g := range gaps {
		if g.Work.DOI == "10.1/known" || g.Work.DOI == "10.1/offtopic" {
			t.Fatalf("gap must exclude known/offtopic work: %+v", g)
		}
		if g.Rationale == "" {
			t.Fatalf("gap must carry a rationale: %+v", g)
		}
	}
}
