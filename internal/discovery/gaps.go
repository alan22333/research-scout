package discovery

import (
	"fmt"
	"sort"
	"strings"
)

var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "by": true, "for": true, "from": true, "in": true, "into": true,
	"is": true, "of": true, "on": true, "or": true, "the": true, "to": true,
	"under": true, "using": true, "via": true, "with": true,
}

const (
	defaultRecentWindow          = 2
	defaultCanonicalMinCitations = 100
	defaultSeedTermMinLength     = 5
	maxRelatedMethodGaps         = 5
	maxLatestProgressGaps        = 5
)

// DetectGaps derives canonical / related-method / latest-progress gaps from
// normalized external works relative to a personal-library projection.
func DetectGaps(in GapInput) []Gap {
	nowYear := in.NowYear
	if nowYear <= 0 {
		nowYear = 2026
	}
	minCanonical := in.CanonicalCitations
	if minCanonical <= 0 {
		minCanonical = defaultCanonicalMinCitations
	}
	byDOI := map[string]LibraryDoc{}
	byTitle := map[string]LibraryDoc{}
	for _, doc := range in.Library {
		if doi := NormalizeDOI(doc.DOI); doi != "" {
			byDOI[doi] = doc
		}
		if fp := FingerprintTitle(doc.Title); fp != "" && doc.Year > 0 {
			byTitle[titleKey(fp, doc.Year)] = doc
		}
	}

	relevantTerms := libraryTermSet(in.Seeds, in.Library)
	hasSeedTerms := len(relevantTerms) > 0

	var canonical, related, latest []Gap
	seen := map[string]bool{}
	for _, w := range MergeAll(in.Works) {
		if w.IdentityKey == "" {
			w.IdentityKey = Key(w)
		}
		key := w.IdentityKey
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true

		doc, inLibrary := LibraryDoc{}, false
		if matched, ok := matchLibrary(w, byDOI, byTitle); ok {
			doc, inLibrary = matched, true
		}
		if inLibrary && !doc.Missing && doc.InScope {
			continue
		}

		missing := MissingPersonalLibrary
		switch {
		case inLibrary:
			// Present in My Library but outside the current scope: still a gap,
			// but a scope gap rather than a library-wide miss.
			missing = MissingCurrentScope
		case in.LibraryUnknown:
			missing = MissingExternalOnly
		}

		switch {
		case isCanonical(w, minCanonical) && missing == MissingPersonalLibrary:
			canonical = append(canonical, Gap{
				Type:              GapCanonical,
				Work:              w,
				MissingRelativeTo: missing,
				Rationale:         canonicalRationale(w, minCanonical),
				NotYetInLibrary:   true,
			})
		case w.Year >= nowYear-defaultRecentWindow && w.Year <= nowYear && (!hasSeedTerms || overlapsTerms(w, relevantTerms)):
			latest = append(latest, Gap{
				Type:              GapLatestProgress,
				Work:              w,
				MissingRelativeTo: missing,
				Rationale:         fmt.Sprintf("published in %d, within the last %d years, and absent from the personal library", w.Year, defaultRecentWindow),
				NotYetInLibrary:   true,
			})
		case relatedMethodTerm(w, in.Seeds) != "":
			term := relatedMethodTerm(w, in.Seeds)
			related = append(related, Gap{
				Type:              GapRelatedMethod,
				Work:              w,
				MissingRelativeTo: missing,
				Rationale:         fmt.Sprintf("shared method term %q with library seeds", term),
				NotYetInLibrary:   true,
			})
		}
	}

	sort.Slice(canonical, func(i, j int) bool { return canonical[i].Work.CitationCount > canonical[j].Work.CitationCount })
	sort.Slice(related, func(i, j int) bool { return related[i].Work.CitationCount > related[j].Work.CitationCount })
	sort.Slice(latest, func(i, j int) bool { return latest[i].Work.Year > latest[j].Work.Year })

	related = capGaps(related, maxRelatedMethodGaps)
	latest = capGaps(latest, maxLatestProgressGaps)
	out := make([]Gap, 0, len(canonical)+len(related)+len(latest))
	out = append(out, canonical...)
	out = append(out, related...)
	out = append(out, latest...)
	return out
}

func capGaps(gaps []Gap, limit int) []Gap {
	if len(gaps) <= limit {
		return gaps
	}
	return gaps[:limit]
}

// LibraryToWork projects an indexed library document into ExternalWork shape so
// that both sides share one identity-key implementation.
func LibraryToWork(doc LibraryDoc) ExternalWork {
	return ExternalWork{
		Title:   doc.Title,
		Year:    doc.Year,
		DOI:     doc.DOI,
		Status:  ExternalStatusAvailable,
		Sources: []DiscoverySource{},
	}
}

func isCanonical(w ExternalWork, minCitations int) bool {
	if strings.EqualFold(w.Type, "review") || strings.EqualFold(w.Type, "survey") {
		return true
	}
	return w.CitationCount >= minCitations
}

func canonicalRationale(w ExternalWork, minCitations int) string {
	if strings.EqualFold(w.Type, "review") || strings.EqualFold(w.Type, "survey") {
		return "review or survey type work absent from the personal library"
	}
	return fmt.Sprintf("citation count %d meets the canonical threshold %d and the work is absent from the personal library", w.CitationCount, minCitations)
}

// libraryTermSet collects the non-trivial title terms of the seeds and library
// documents so that recent-but-off-topic works are not reported as gaps.
func libraryTermSet(seeds, library []LibraryDoc) map[string]bool {
	terms := map[string]bool{}
	for _, doc := range append(append([]LibraryDoc{}, seeds...), library...) {
		for _, t := range methodTerms(doc.Title) {
			terms[t] = true
		}
	}
	return terms
}

func overlapsTerms(w ExternalWork, terms map[string]bool) bool {
	for _, t := range methodTerms(w.Title) {
		if terms[t] {
			return true
		}
	}
	return false
}

// methodTerms extracts deterministic, non-trivial title terms used to relate an
// external work to library seeds.
func methodTerms(title string) []string {
	fp := FingerprintTitle(title)
	var out []string
	for _, term := range strings.Fields(fp) {
		if len([]rune(term)) < defaultSeedTermMinLength || stopWords[term] {
			continue
		}
		out = append(out, term)
	}
	return out
}

func relatedMethodTerm(work ExternalWork, seeds []LibraryDoc) string {
	workTerms := map[string]bool{}
	for _, t := range methodTerms(work.Title) {
		workTerms[t] = true
	}
	for _, seed := range seeds {
		for _, t := range methodTerms(seed.Title) {
			if workTerms[t] {
				return t
			}
		}
	}
	return ""
}
