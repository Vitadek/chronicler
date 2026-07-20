package grammar

import (
	"sort"
	"strings"
	"unicode"
)

// Suggestion generation.
//
// gospell gives us membership testing (Spell) but no Suggest, so this is the
// half we supply. It is the classic Norvig approach inverted for speed: rather
// than scanning the dictionary for near-matches, generate every string within
// one edit of the misspelling and keep those the dictionary already accepts.
// For an n-letter word that is ~54n+25 candidates, each a map lookup — tens of
// microseconds, which matters because this runs per unknown word on a path the
// editor hits per paragraph.

const suggestAlphabet = "abcdefghijklmnopqrstuvwxyz"

// Suggest returns up to maxReplacements corrections, best first.
//
// Edit distance 2 is attempted only when distance 1 yields nothing: it is ~400x
// more candidates, and for a word that far from any real one the guesses are
// rarely useful anyway.
func (d *Dictionary) Suggest(word string) []string {
	if word == "" {
		return nil
	}
	lower := strings.ToLower(normalizeApostrophes(word))

	cands := d.knownEdits(edits1(lower))
	if len(cands) == 0 {
		cands = d.knownEdits(edits2(lower))
	}
	if len(cands) == 0 {
		return nil
	}

	ranked := d.rankCandidates(lower, cands)
	if len(ranked) > maxReplacements {
		ranked = ranked[:maxReplacements]
	}
	// Give suggestions the shape of the original: "Teh" -> "The", not "the".
	for i := range ranked {
		ranked[i] = matchCase(word, ranked[i])
	}
	return ranked
}

// knownEdits keeps only candidates the dictionary accepts.
func (d *Dictionary) knownEdits(cands map[string]struct{}) []string {
	var out []string
	for c := range cands {
		if c == "" {
			continue
		}
		if d.spell(c) {
			out = append(out, c)
		}
	}
	return out
}

// rankCandidates orders corrections by how likely a writer meant them.
//
// Corpus frequency dominates — for "teh" that is what puts "the" ahead of
// "ten" and "tea", matching what LanguageTool returned. Two tie-breakers:
// an unranked candidate (outside the top 50k) always loses to a ranked one,
// and a preserved first letter wins, since typos rarely land on the first
// keystroke.
func (d *Dictionary) rankCandidates(original string, cands []string) []string {
	type scored struct {
		word     string
		rank     int
		ranked   bool
		sameHead bool
	}
	items := make([]scored, 0, len(cands))
	for _, c := range cands {
		r, ok := d.rankOf(c)
		items = append(items, scored{
			word:     c,
			rank:     r,
			ranked:   ok,
			sameHead: len(c) > 0 && len(original) > 0 && c[0] == original[0],
		})
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.ranked != b.ranked {
			return a.ranked
		}
		if a.sameHead != b.sameHead {
			return a.sameHead
		}
		if a.ranked && b.ranked && a.rank != b.rank {
			return a.rank < b.rank
		}
		if len(a.word) != len(b.word) {
			return len(a.word) < len(b.word)
		}
		return a.word < b.word // deterministic output
	})

	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.word
	}
	return out
}

// edits1 returns every string one edit from word: deletions, transpositions,
// replacements and insertions.
func edits1(word string) map[string]struct{} {
	r := []rune(word)
	out := make(map[string]struct{}, 54*len(r)+25)

	for i := 0; i <= len(r); i++ {
		// deletion
		if i < len(r) {
			out[string(r[:i])+string(r[i+1:])] = struct{}{}
		}
		// transposition
		if i < len(r)-1 {
			out[string(r[:i])+string(r[i+1])+string(r[i])+string(r[i+2:])] = struct{}{}
		}
		for _, c := range suggestAlphabet {
			// replacement
			if i < len(r) {
				out[string(r[:i])+string(c)+string(r[i+1:])] = struct{}{}
			}
			// insertion
			out[string(r[:i])+string(c)+string(r[i:])] = struct{}{}
		}
	}
	delete(out, word)
	return out
}

// edits2 returns strings two edits away. Only reached when edits1 found
// nothing in the dictionary.
func edits2(word string) map[string]struct{} {
	out := make(map[string]struct{})
	for e1 := range edits1(word) {
		for e2 := range edits1(e1) {
			out[e2] = struct{}{}
		}
	}
	delete(out, word)
	return out
}

// matchCase reshapes a lowercase suggestion to the original's capitalization.
func matchCase(original, suggestion string) string {
	if original == "" || suggestion == "" {
		return suggestion
	}
	origRunes := []rune(original)
	// ALL CAPS -> ALL CAPS
	if len(origRunes) > 1 && original == strings.ToUpper(original) {
		return strings.ToUpper(suggestion)
	}
	// Capitalized -> Capitalized
	if unicode.IsUpper(origRunes[0]) {
		sugRunes := []rune(suggestion)
		return string(unicode.ToUpper(sugRunes[0])) + string(sugRunes[1:])
	}
	return suggestion
}
