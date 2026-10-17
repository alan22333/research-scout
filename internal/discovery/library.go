package discovery

import "strings"

// DedupAgainstLibrary marks external works that already exist in the personal
// library. Library matches never become gaps on their own.
func DedupAgainstLibrary(works []ExternalWork, library []LibraryDoc) []ExternalWork {
	if len(works) == 0 {
		return nil
	}
	byDOI := map[string]LibraryDoc{}
	byTitle := map[string]LibraryDoc{}
	for _, doc := range library {
		if doi := NormalizeDOI(doc.DOI); doi != "" {
			byDOI[doi] = doc
		}
		if fp := FingerprintTitle(doc.Title); fp != "" && doc.Year > 0 {
			byTitle[titleKey(fp, doc.Year)] = doc
		}
	}
	out := make([]ExternalWork, 0, len(works))
	for _, w := range works {
		if doc, ok := matchLibrary(w, byDOI, byTitle); ok {
			w.AlreadyInLibrary = true
			w.LibraryDocumentID = doc.DocumentID
			w.InCurrentScope = doc.InScope
		}
		out = append(out, w)
	}
	return out
}

func titleKey(fingerprint string, year int) string {
	return fingerprint + "|" + itoa(year)
}

func matchLibrary(w ExternalWork, byDOI map[string]LibraryDoc, byTitle map[string]LibraryDoc) (LibraryDoc, bool) {
	if doi := NormalizeDOI(w.DOI); doi != "" {
		if doc, ok := byDOI[doi]; ok {
			return doc, true
		}
	}
	fp := FingerprintTitle(w.Title)
	if fp == "" || w.Year <= 0 {
		return LibraryDoc{}, false
	}
	doc, ok := byTitle[titleKey(fp, w.Year)]
	return doc, ok
}

// TitleMatches reports whether two works share a normalized title and year.
func TitleMatches(a, b ExternalWork) bool {
	if a.Year <= 0 || b.Year <= 0 || a.Year != b.Year {
		return false
	}
	fa, fb := FingerprintTitle(a.Title), FingerprintTitle(b.Title)
	return fa != "" && fa == fb
}

// ReferencedByLibrary reports whether a work is present in a library projection.
func ReferencedByLibrary(w ExternalWork, library []LibraryDoc) bool {
	for _, doc := range library {
		if doi := NormalizeDOI(doc.DOI); doi != "" {
			if doi == NormalizeDOI(w.DOI) {
				return true
			}
		}
		if strings.EqualFold(FingerprintTitle(doc.Title), FingerprintTitle(w.Title)) && doc.Year == w.Year {
			return true
		}
	}
	return false
}
