package grammar

import "fmt"

// checkSpelling reports unknown words as KindMisspelling, with corrections.
//
// Deliberately conservative — a false positive in a novel is worse than a miss,
// because writers use invented names and archaic diction constantly, and every
// red squiggle under a deliberate coinage trains them to ignore the checker:
//
//   - Words the confusion table already claimed are skipped, so "quiet" in
//     "quiet nice" is reported once as a word-choice issue rather than twice.
//   - Capitalized words mid-sentence are left alone unless they are also
//     unknown in lowercase. Invented proper nouns ("Vexhollow", "Katherine")
//     are the single largest source of false positives in fiction, and the
//     user's custom dictionary is a client-side concept this package cannot
//     see (chronicle/src/lib/dictionary.ts filters our output).
func (d *Dictionary) checkSpelling(text string, toks []token, claimed map[int]bool) []Hit {
	var hits []Hit
	for _, t := range toks {
		if claimed[t.Start] {
			continue
		}
		if d.Known(t.Word) {
			continue
		}
		if isLikelyProperNoun(t.Word) {
			continue
		}
		hits = append(hits, Hit{
			Start:        t.Start,
			End:          t.End,
			Kind:         KindMisspelling,
			Message:      fmt.Sprintf("%q may be misspelled.", t.Word),
			Replacements: emptyIfNil(d.Suggest(t.Word)),
		})
	}
	return hits
}

// isLikelyProperNoun treats a capitalized, non-all-caps word as a name.
//
// This is the fiction-specific escape hatch: it means invented names never
// squiggle, at the cost of missing a genuinely misspelled capitalized word.
// That trade is deliberate — see checkSpelling.
func isLikelyProperNoun(word string) bool {
	r := []rune(word)
	if len(r) < 2 {
		return false
	}
	if r[0] < 'A' || r[0] > 'Z' {
		return false
	}
	// "HELLO" is emphasis, not a name — let it be spell-checked.
	for _, c := range r[1:] {
		if c >= 'a' && c <= 'z' {
			return true
		}
	}
	return false
}

// emptyIfNil keeps the JSON contract: `replacements` is always an array,
// never null, because ProofreadView indexes it without a guard.
func emptyIfNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
