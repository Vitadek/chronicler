package grammar

import "sort"

// Check runs every checker over text and returns the findings.
//
// Order matters. Confusion runs first and "claims" the tokens it reports, so a
// correctly spelled but misused word ("quiet" in "quiet nice") is reported once
// as word-choice rather than also being second-guessed by spelling or grammar.
// Each later checker skips claimed tokens for the same reason: one squiggle per
// problem, or the Issues pane fills with duplicates of the same underlying
// mistake.
//
// The result is sorted by position so the frontend can render in document order
// without sorting again, and is never nil — the route serializes it directly
// and `{"hits": null}` would break the client's `data.hits || []` narrowing.
func (d *Dictionary) Check(text string) []Hit {
	if text == "" {
		return []Hit{}
	}

	toks := tokenize(text)
	if len(toks) == 0 {
		return []Hit{}
	}

	hits, claimed := d.checkConfusion(toks)
	hits = append(hits, d.checkGrammar(toks, claimed)...)
	hits = append(hits, d.checkSpelling(text, toks, claimed)...)
	hits = append(hits, d.checkStyle(text, toks, claimed)...)

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Start != hits[j].Start {
			return hits[i].Start < hits[j].Start
		}
		return hits[i].End < hits[j].End
	})

	if hits == nil {
		return []Hit{}
	}
	return hits
}
