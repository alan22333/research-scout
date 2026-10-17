package query

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"research-scout/internal/domain"
	"research-scout/internal/storage"
)

type briefContent struct {
	Summary       string
	SummaryClaims []ClaimView
	Findings      []FindingView
	OpenQuestions []string
}

// groundingRow is one scientific evidence chunk considered by the grounding
// policy. Web context never appears here: it can never carry a scientific
// conclusion, so it is reported separately as background.
type groundingRow struct {
	view    EvidenceView
	chunk   retrievedChunk
	negated bool
	terms   map[string]bool
}

// buildBriefContent applies the frozen Stage 3 evidence policy.
//
//   - personal_fulltext can support verified claims;
//   - structured_abstract is always partial;
//   - web_context is background only: it is persisted as evidence but never
//     produces a finding and never makes a claim verified.
//
// When only web context is available the brief abstains instead of inventing a
// finding. When two scientific evidence groups disagree (one carries a negation
// marker and the other does not, in the same topic cluster) both stances are
// retained and an open question is recorded. Conflicts are never resolved by
// citation counts or by voting.
//
// Evidence ids are derived from the brief id, the source chunk id and the
// chunk ordinal so that the generated view and the persisted rows use the exact
// same identity.
func buildBriefContent(briefID, question string, chunks []retrievedChunk) briefContent {
	out := briefContent{
		Findings:      []FindingView{},
		SummaryClaims: []ClaimView{},
		OpenQuestions: []string{},
	}
	rows := make([]groundingRow, 0, len(chunks))
	webContexts := 0
	for i, chunk := range chunks {
		switch chunk.EvidenceType {
		case domain.EvidencePersonalFulltext, domain.EvidenceStructuredAbstract:
			evidenceID := evidenceIDFor(briefID, chunk.ChunkID, i)
			locator := locatorFor(chunk)
			quote := truncate(chunk.Content, 360)
			view := EvidenceView{
				ID: evidenceID, EvidenceType: string(chunk.EvidenceType), Title: chunk.Title,
				Quote: stringPtr(quote), DocumentID: stringPtr(chunk.DocumentID),
				DocumentVersionID: stringPtr(chunk.DocumentVersionID), ChunkID: stringPtr(chunk.ChunkID),
				Locator: locator, Score: chunk.Score,
			}
			if chunk.ExternalWorkID != "" {
				view.ExternalWorkID = stringPtr(chunk.ExternalWorkID)
			}
			rows = append(rows, groundingRow{
				view: view, chunk: chunk, negated: hasNegation(chunk.Content), terms: significantTerms(chunk.Content),
			})
		case domain.EvidenceWebContext:
			webContexts++
		}
	}

	if len(rows) == 0 {
		out.Summary = "Insufficient scientific evidence in the current scope to answer this question."
		if webContexts > 0 {
			out.OpenQuestions = append(out.OpenQuestions, "Web context was retrieved as background only; it cannot support a scientific conclusion. Structured evidence is still required.")
		}
		out.OpenQuestions = append(out.OpenQuestions, "The retrieved evidence does not contain a scientific basis for a grounded answer.")
		return out
	}

	// Deterministic conflict grouping: cluster scientific rows by shared
	// significant terms, then flag clusters that contain both a negated and a
	// non-negated statement. Conflict stance never downgrades web context to
	// scientific evidence and never uses citation counts.
	conflicting, conflictTopics := conflictClusters(rows)

	verifiedSummary := false
	summaryEvidence := make([]ClaimEvidenceView, 0, len(rows))
	for _, row := range rows {
		status := domain.VerificationPartial
		support := "partial"
		if row.chunk.EvidenceType == domain.EvidencePersonalFulltext {
			status = domain.VerificationVerified
			support = "sufficient"
			verifiedSummary = true
		}
		stance := domain.ClaimSupports
		if conflicting[row.view.ID] {
			stance = domain.ClaimRefutes
		}
		span := truncate(row.chunk.Content, 220)
		relation := ClaimEvidenceView{
			EvidenceID: row.view.ID, Stance: string(stance),
			SupportScore: clamp01(row.chunk.Score), EvidenceSpan: stringPtr(span), Locator: row.view.Locator,
		}
		summaryEvidence = append(summaryEvidence, relation)
		excerpt := truncate(strings.TrimSpace(row.chunk.Content), 260)
		findingText := fmt.Sprintf("Evidence from %s: %s", row.chunk.Title, excerpt)
		out.Findings = append(out.Findings, FindingView{
			ClaimID:   storage.StableID("claim", "finding", row.view.ID),
			ClaimType: string(domain.ClaimFinding), Claim: findingText,
			SupportLevel: support, Confidence: clamp01(row.chunk.Score),
			VerificationStatus: string(status), EvidenceIDs: []string{row.view.ID},
			ClaimEvidence: []ClaimEvidenceView{relation},
		})
	}

	summaryStatus := domain.VerificationPartial
	summarySupport := "partial"
	if verifiedSummary {
		summaryStatus = domain.VerificationVerified
		summarySupport = "sufficient"
	}
	out.Summary = fmt.Sprintf("Evidence from %d relevant passage(s) was retrieved for: %s", len(rows), question)
	out.SummaryClaims = append(out.SummaryClaims, ClaimView{
		ClaimID:   storage.StableID("claim", "summary", briefID),
		ClaimType: string(domain.ClaimSummary), Text: out.Summary,
		SupportLevel: summarySupport, Confidence: confidenceForRows(rows),
		VerificationStatus: string(summaryStatus), ClaimEvidence: summaryEvidence,
	})
	if len(conflictTopics) > 0 {
		out.OpenQuestions = append(out.OpenQuestions, fmt.Sprintf(
			"Evidence presents conflicting stances on %s; both sides are retained and neither is resolved by citation count or source voting.",
			strings.Join(conflictTopics, ", ")))
	}
	if !verifiedSummary {
		out.OpenQuestions = append(out.OpenQuestions, "Full-text evidence is still needed to verify the abstract-level conclusion.")
	}
	return out
}

// conflictClusters returns the evidence ids that sit in a conflicting topic
// cluster and the ordered list of topic labels. A cluster is conflicting when
// it contains at least one negated and one non-negated statement.
func conflictClusters(rows []groundingRow) (map[string]bool, []string) {
	type cluster struct {
		terms   map[string]bool
		negated []string
		plain   []string
		topic   string
	}
	clusters := make([]*cluster, 0, len(rows))
	for _, row := range rows {
		var target *cluster
		for _, c := range clusters {
			if sharesTerm(c.terms, row.terms) {
				target = c
				break
			}
		}
		if target == nil {
			target = &cluster{terms: map[string]bool{}, topic: primaryTerm(row.terms)}
			clusters = append(clusters, target)
		}
		for t := range row.terms {
			target.terms[t] = true
		}
		if row.negated {
			target.negated = append(target.negated, row.view.ID)
		} else {
			target.plain = append(target.plain, row.view.ID)
		}
	}
	conflicting := map[string]bool{}
	topics := make([]string, 0, len(clusters))
	for _, c := range clusters {
		if len(c.negated) == 0 || len(c.plain) == 0 {
			continue
		}
		// Only the negated statements take the refuting stance; the
		// non-negated statements keep supporting. Both remain in the brief.
		for _, id := range c.negated {
			conflicting[id] = true
		}
		topics = append(topics, c.topic)
	}
	sort.Strings(topics)
	return conflicting, topics
}

func sharesTerm(a, b map[string]bool) bool {
	for t := range a {
		if b[t] {
			return true
		}
	}
	return false
}

func primaryTerm(terms map[string]bool) string {
	if len(terms) == 0 {
		return "the described method"
	}
	list := make([]string, 0, len(terms))
	for t := range terms {
		list = append(list, t)
	}
	sort.Strings(list)
	return list[0]
}

// confidenceForRows averages the retrieval scores of the scientific rows that
// back a summary claim. It never reads web context scores.
func confidenceForRows(rows []groundingRow) float64 {
	if len(rows) == 0 {
		return 0
	}
	total := 0.0
	for _, row := range rows {
		total += clamp01(row.chunk.Score)
	}
	return total / float64(len(rows))
}

var (
	negationEnglish = regexp.MustCompile(`(?i)\b(not|no|without|fail|fails|failed|does not|do not|did not|cannot|can not|unable|never)\b`)
	negationCJK     = regexp.MustCompile(`不|无法|未能|不能|没有|并非`)
	termPattern     = regexp.MustCompile(`[\p{L}\p{N}]+`)
)

// hasNegation reports whether text carries a negation/refutation marker from the
// frozen marker set.
func hasNegation(text string) bool {
	return negationEnglish.MatchString(text) || negationCJK.MatchString(text)
}

// significantTerms extracts the lowercase content tokens used for deterministic
// topic clustering. Tokens shorter than four runes and common English stopwords
// are dropped so clustering keys on meaningful method/topic words.
func significantTerms(text string) map[string]bool {
	out := map[string]bool{}
	for _, raw := range termPattern.FindAllString(strings.ToLower(text), -1) {
		if len([]rune(raw)) < 4 {
			continue
		}
		if stopwords[raw] {
			continue
		}
		out[raw] = true
	}
	return out
}

var stopwords = map[string]bool{
	"this": true, "that": true, "with": true, "from": true, "these": true,
	"those": true, "their": true, "there": true, "which": true, "while": true,
	"when": true, "than": true, "then": true, "into": true, "over": true,
	"such": true, "also": true, "have": true, "has": true, "been": true,
	"were": true, "will": true, "would": true, "could": true, "should": true,
	"they": true, "them": true, "more": true, "most": true, "some": true,
	"using": true, "used": true, "uses": true, "does": true, "each": true,
	"both": true, "between": true, "against": true, "about": true,
}

func evidenceIDFor(briefID, chunkID string, ordinal int) string {
	return storage.StableID("evidence", briefID, chunkID, fmt.Sprint(ordinal))
}

func locatorFor(chunk retrievedChunk) map[string]any {
	locator := map[string]any{
		"chunk_id": chunk.ChunkID, "document_version_id": chunk.DocumentVersionID,
		"section": chunk.Section, "char_start": chunk.CharStart, "char_end": chunk.CharEnd,
	}
	if chunk.PageStart != nil {
		locator["page_start"] = *chunk.PageStart
	}
	if chunk.PageEnd != nil {
		locator["page_end"] = *chunk.PageEnd
	}
	return locator
}
