package domain

import (
	"strings"
	"unicode"
)

// order.go holds the canonical "what's next" ordering comparator and the title
// normalization/scoring used for duplicate detection (ARCHITECTURE §2). The
// SQL twin is the idx_task_order index — docs/ARCHITECTURE.md §3 uses exactly
// this expression in every ORDER BY: priority DESC, (due_at IS NULL),
// due_at ASC, created_at ASC.

// CanonicalLess reports whether task a sorts before task b in the canonical
// "what's next" ordering: priority DESC, then due_at ASC with nil/zero due
// dates LAST (SQLite has no NULLS LAST keyword — `(due_at IS NULL)` sorts 0
// before 1), then created_at ASC as the final tiebreak. It is the single Go
// definition of the order; adapters' fakes and tests must both use it so the
// in-memory and SQL orders can never drift apart. Strict weak ordering: for
// two tasks equal on all three keys it reports false in BOTH directions.
func CanonicalLess(a, b Task) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority // priority DESC
	}
	aNil, bNil := a.DueAt == nil, b.DueAt == nil
	if aNil || bNil {
		return !aNil // non-null due dates sort before nulls (nulls last)
	}
	if !a.DueAt.Equal(*b.DueAt) {
		return a.DueAt.Before(*b.DueAt) // due_at ASC
	}
	return a.CreatedAt.Before(b.CreatedAt) // created_at ASC, final tiebreak
}

// duplicateStopwords carry no identity in a task title and are dropped before
// token comparison. Punctuation is not a stopword — the tokenizer strips it
// (tokens are alphanumeric runs).
var duplicateStopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "as": {}, "are": {}, "at": {}, "be": {},
	"by": {}, "for": {}, "from": {}, "in": {}, "is": {}, "it": {}, "on": {},
	"or": {}, "the": {}, "to": {}, "was": {}, "with": {},
}

// NormalizeTitle is the canonical comparison form for duplicate detection:
// lowercased + trimmed (ARCHITECTURE §4's "normalized-equal title").
func NormalizeTitle(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// SignificantTokens splits a NORMALIZED title (NormalizeTitle's output — it
// must already be lowercase and trimmed) into alphanumeric runs, drops
// stopwords, and returns the surviving set. Runs are split on any run of other
// characters, so punctuation never leaks into a token ("auth!" → "auth").
func SignificantTokens(normalizedTitle string) map[string]struct{} {
	tokens := make(map[string]struct{})
	var run []rune
	flush := func() {
		if len(run) > 0 {
			token, r := string(run), []rune(nil)
			run = r
			if _, stop := duplicateStopwords[token]; !stop {
				tokens[token] = struct{}{}
			}
		}
	}
	for _, c := range normalizedTitle {
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			run = append(run, c)
			continue
		}
		flush()
	}
	flush()
	return tokens
}

// TitleSimilarity ranks a candidate against a query on an integer scale (higher
// = more similar; 0 = not a candidate). Both normalized forms are
// NormalizeTitle output:
//
//	3 — normalized-equal titles ("Refactor auth" vs " refactor auth ")
//	2 — one normalized title contains the other, either direction
//	   ("refactor auth" ⊂ "refactor authentication")
//	1 — at least half of the query's significant tokens occur in the candidate
//	   ("refactor auth!" vs "refactor authorization flow": both carry "refactor")
//	0 — no containment and fewer than half the query's tokens shared
func TitleSimilarity(queryNorm, candNorm string, qTokens, cTokens map[string]struct{}) int {
	if queryNorm == "" {
		return 0 // nothing to match on
	}
	if candNorm == queryNorm {
		return 3
	}
	if strings.Contains(candNorm, queryNorm) || strings.Contains(queryNorm, candNorm) {
		return 2
	}
	if len(qTokens) > 0 {
		shared := 0
		for tok := range qTokens {
			if _, ok := cTokens[tok]; ok {
				shared++
			}
		}
		if 2*shared >= len(qTokens) { // ≥ half of the query's significant tokens
			return 1
		}
	}
	return 0
}
