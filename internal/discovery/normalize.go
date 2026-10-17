package discovery

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var doiPrefixes = []string{
	"https://doi.org/",
	"http://doi.org/",
	"https://dx.doi.org/",
	"http://dx.doi.org/",
	"doi:",
}

// NormalizeDOI lowercases a DOI and strips URL/prefix decorations.
func NormalizeDOI(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, "<>")
	for {
		lower := strings.ToLower(s)
		matched := false
		for _, prefix := range doiPrefixes {
			if strings.HasPrefix(lower, prefix) {
				s = strings.TrimSpace(s[len(prefix):])
				matched = true
				break
			}
		}
		if !matched {
			break
		}
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".")
	return strings.ToLower(s)
}

// FingerprintTitle produces a stable title fingerprint. Punctuation becomes a
// separator so that adjacent words are never fused together.
func FingerprintTitle(title string) string {
	s := norm.NFKC.String(title)
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func shortID(prefix, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return prefix + ":" + value
}

// Key returns the canonical identity key using the frozen priority order:
// DOI -> S2 ID -> OpenAlex ID -> PMID -> title fingerprint + year.
func Key(w ExternalWork) string {
	if doi := NormalizeDOI(w.DOI); doi != "" {
		return "doi:" + doi
	}
	if id := strings.ToLower(strings.TrimSpace(w.S2ID)); id != "" {
		return "s2:" + id
	}
	if id := strings.ToLower(normalizeOpenAlexID(w.OpenAlexID)); id != "" {
		return "openalex:" + id
	}
	if id := strings.ToLower(strings.TrimSpace(w.PMID)); id != "" {
		return "pmid:" + id
	}
	fp := FingerprintTitle(w.Title)
	if fp == "" || w.Year <= 0 {
		return ""
	}
	return "title:" + fp + ":" + itoa(w.Year)
}

func normalizeOpenAlexID(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		s = s[idx+1:]
	}
	return s
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// completeness scores how much usable metadata a record carries. It decides
// which duplicate record survives a merge.
func completeness(w ExternalWork) int {
	score := 0
	if strings.TrimSpace(w.Abstract) != "" {
		score += 4
	}
	if strings.TrimSpace(w.Venue) != "" {
		score++
	}
	if w.Year > 0 {
		score++
	}
	if len(w.Authors) > 0 {
		score++
	}
	if w.CitationCount > 0 {
		score++
	}
	if strings.TrimSpace(w.Type) != "" {
		score++
	}
	return score
}

func preferWork(a, b ExternalWork) bool {
	sa, sb := completeness(a), completeness(b)
	if sa != sb {
		return sa > sb
	}
	if (strings.TrimSpace(a.Abstract) != "") != (strings.TrimSpace(b.Abstract) != "") {
		return strings.TrimSpace(a.Abstract) != ""
	}
	if a.CitationCount != b.CitationCount {
		return a.CitationCount > b.CitationCount
	}
	return a.RetrievedAt.After(b.RetrievedAt)
}

// Merge combines two records that share an identity key, preferring the more
// complete record while filling any gaps from the other.
func Merge(a, b ExternalWork) ExternalWork {
	primary, secondary := a, b
	if preferWork(b, a) {
		primary, secondary = b, a
	}
	out := primary
	if out.DOI == "" {
		out.DOI = NormalizeDOI(secondary.DOI)
	}
	if out.S2ID == "" {
		out.S2ID = secondary.S2ID
	}
	if out.OpenAlexID == "" {
		out.OpenAlexID = normalizeOpenAlexID(secondary.OpenAlexID)
	}
	if out.PMID == "" {
		out.PMID = secondary.PMID
	}
	if out.Abstract == "" {
		out.Abstract = secondary.Abstract
	}
	if out.Venue == "" {
		out.Venue = secondary.Venue
	}
	if out.Year <= 0 {
		out.Year = secondary.Year
	}
	if len(out.Authors) == 0 {
		out.Authors = secondary.Authors
	}
	if out.CitationCount < secondary.CitationCount {
		out.CitationCount = secondary.CitationCount
	}
	if out.Type == "" {
		out.Type = secondary.Type
	}
	out.Sources = mergeSources(primary.Sources, secondary.Sources, primary.Source, secondary.Source)
	if out.Status == ExternalStatusInvalid {
		out.Status = ExternalStatusDegraded
	}
	if out.IdentityKey == "" {
		out.IdentityKey = Key(out)
	}
	if out.ID == "" {
		out.ID = out.IdentityKey
	}
	if out.RetrievedAt.IsZero() {
		out.RetrievedAt = secondary.RetrievedAt
	}
	return out
}

func mergeSources(a, b []DiscoverySource, single ...DiscoverySource) []DiscoverySource {
	seen := map[DiscoverySource]bool{}
	var out []DiscoverySource
	add := func(s DiscoverySource) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range a {
		add(s)
	}
	for _, s := range b {
		add(s)
	}
	for _, s := range single {
		add(s)
	}
	return out
}

// MergeAll collapses duplicate records in one deterministic pass.
func MergeAll(works []ExternalWork) []ExternalWork {
	var out []ExternalWork
	index := map[string]int{}
	for _, w := range works {
		key := w.IdentityKey
		if key == "" {
			key = Key(w)
			w.IdentityKey = key
		}
		if key == "" {
			out = append(out, w)
			continue
		}
		if pos, ok := index[key]; ok {
			merged := Merge(out[pos], w)
			out[pos] = merged
			index[merged.IdentityKey] = pos
			continue
		}
		index[key] = len(out)
		out = append(out, w)
	}
	return out
}
