package discovery

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

const (
	sourceStatusAvailable   = "available"
	sourceStatusDegraded    = "degraded"
	sourceStatusUnavailable = "unavailable"
	sourceStatusSkipped     = "skipped"
)

// cleanText strips NUL/control characters that some provider payloads contain
// while preserving ordinary whitespace.
func cleanText(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\t' || r == '\r' {
			b.WriteRune(' ')
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// asString coerces a provider field that may be encoded as a string or a
// number (for example Semantic Scholar externalIds mixes DOI strings with
// numeric CorpusId values) into a plain string.
func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprint(t)
	}
}

// sanitizeJSONControlBytes removes raw control characters that are illegal
// anywhere in JSON (everything below 0x20 except tab/newline/CR whitespace).
// Semantic Scholar occasionally returns abstracts containing stray control
// bytes; without this the whole payload fails json.Unmarshal and a valid
// result set is discarded. Escaped control characters are left untouched.
func sanitizeJSONControlBytes(data []byte) []byte {
	clean := true
	for _, b := range data {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' {
			clean = false
			break
		}
	}
	if clean {
		return data
	}
	out := make([]byte, 0, len(data))
	for _, b := range data {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' {
			continue
		}
		out = append(out, b)
	}
	return out
}

func errorReason(err error) string {
	var pe *ProviderError
	if AsProviderError(err, &pe) {
		return string(pe.Code)
	}
	if err == nil {
		return ""
	}
	return string(ProviderUnavailable)
}

// AsProviderError is a tiny local wrapper so call sites stay readable without
// importing errors everywhere.
func AsProviderError(err error, target **ProviderError) bool {
	if err == nil {
		return false
	}
	for err != nil {
		if pe, ok := err.(*ProviderError); ok {
			*target = pe
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
