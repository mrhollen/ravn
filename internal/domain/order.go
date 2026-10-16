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
	switch {
	case aNil && bNil:
		// Both in the nulls-last group: created_at is STILL the final tiebreak
		// (the SQL `(due_at IS NULL), due_at ASC, created_at ASC` orders within
		// the null group by created_at too — a nil due date must not make two
		// tasks mutually "equal" before their creation instant).
		return a.CreatedAt.Before(b.CreatedAt)
	case aNil || bNil:
		return !aNil // non-null due dates sort before nulls (nulls last)
	default:
		if !a.DueAt.Equal(*b.DueAt) {
			return a.DueAt.Before(*b.DueAt) // due_at ASC
		}
		return a.CreatedAt.Before(b.CreatedAt) // created_at ASC, final tiebreak
	}
}

// duplicateStopwords are the function words that carry no identity in a task
// title and are dropped before token comparison. The set is a fixed,
// deterministic superset of common English articles, determiners,
// prepositions, particles, conjunctions, auxiliaries, copula forms, negations
// and pronouns: any of these may appear (or be absent) between two titles
// without changing what the titles are about, so junk tokens like "of" must
// never rank ("Fix of auth" is scored on {fix, auth}, not on {of}).
// Punctuation is not a stopword — the tokenizer strips it (tokens are
// alphanumeric runs).
var duplicateStopwords = map[string]struct{}{
	// articles & determiners
	"a": {}, "an": {}, "the": {}, "all": {}, "any": {}, "every": {}, "some": {},
	// prepositions & particles
	"about": {}, "after": {}, "at": {}, "before": {}, "between": {}, "by": {},
	"during": {}, "for": {}, "from": {}, "in": {}, "into": {}, "near": {},
	"of": {}, "off": {}, "on": {}, "onto": {}, "out": {}, "over": {}, "per": {},
	"since": {}, "to": {}, "through": {}, "under": {}, "until": {}, "up": {},
	"upon": {}, "via": {}, "with": {},
	// conjunctions, relatives & common adverbs
	"and": {}, "as": {}, "because": {}, "but": {}, "how": {}, "if": {}, "nor": {},
	"or": {}, "so": {}, "than": {}, "that": {}, "then": {}, "there": {},
	"these": {}, "this": {}, "those": {}, "what": {}, "when": {}, "where": {},
	"which": {}, "who": {}, "whom": {}, "why": {}, "yet": {},
	// auxiliaries, modals & copula forms
	"am": {}, "are": {}, "be": {}, "been": {}, "being": {}, "can": {},
	"could": {}, "did": {}, "do": {}, "does": {}, "done": {}, "had": {},
	"has": {}, "have": {}, "having": {}, "is": {}, "may": {}, "might": {},
	"must": {}, "shall": {}, "should": {}, "was": {}, "were": {}, "will": {},
	"would": {},
	// negations & quantifiers
	"no": {}, "none": {}, "non": {}, "not": {},
	// pronouns
	"he": {}, "her": {}, "hers": {}, "him": {}, "his": {}, "i": {}, "it": {},
	"its": {}, "me": {}, "my": {}, "our": {}, "ours": {}, "she": {},
	"them": {}, "their": {}, "theirs": {}, "us": {}, "we": {}, "you": {},
	"your": {}, "yours": {},
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
// NormalizeTitle output, and both token sets are SignificantTokens of their
// respective normalized title. Scoring operates on token SETS, never on byte
// strings, and the levels are monotonic — full match > containment > partial
// overlap > none:
//
//	3 — normalized-equal titles ("Refactor auth" vs " refactor AUTH ")
//	2 — whole-token containment, either direction: every significant token of
//	   ONE title appears as a WHOLE token in the other (a proper subset on the
//	   token sets). "auth fix" ⊂ "fix auth bug". This is deliberately NOT
//	   byte-string containment: a short title's words must survive as complete
//	   tokens in the longer one, so "ui" does not match "build suite", "api"
//	   does not match "rapid response", and "auth" does not match
//	   "authentication". Equal token sets in different word order are NOT
//	   containment — they fall to the half-token rule below (score 1), exactly
//	   as before the word-boundary change.
//	1 — at least half of the query's significant tokens occur in the candidate
//	   ("refactor auth!" vs "Refactor authentication": both carry "refactor")
//	0 — none of the above; titles with an empty token set (stopword-only) can
//	   never score, in any direction
func TitleSimilarity(queryNorm, candNorm string, qTokens, cTokens map[string]struct{}) int {
	if queryNorm == "" || candNorm == "" {
		return 0 // nothing to match on
	}
	if candNorm == queryNorm {
		return 3
	}
	if tokenProperSubset(qTokens, cTokens) || tokenProperSubset(cTokens, qTokens) {
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

// tokenProperSubset reports whether sub is a PROPER subset of super: non-empty,
// every token of sub also occurs in super, and super carries at least one
// token sub lacks. Equal (reordered) sets are NOT containment — that case
// falls through to the half-token rule, which scores it 1, preserving the
// pre-word-boundary ranking for reordered titles; an empty set is never a
// container or a contained set (a stopword-only title has no identity).
func tokenProperSubset(sub, super map[string]struct{}) bool {
	if len(sub) == 0 || len(sub) >= len(super) {
		return false
	}
	for tok := range sub {
		if _, ok := super[tok]; !ok {
			return false
		}
	}
	return true
}
