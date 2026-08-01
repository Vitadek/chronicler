package grammar

import (
	"sort"
	"strings"
	"unicode"
)

// Suggestion generation.
//
// gospell gives us membership testing (Spell) but no Suggest, so this is the
// half we supply. One-edit typos use the classic Norvig approach: generate the
// ~54n+25 neighboring strings and keep those the dictionary accepts. If none
// match, distance-two suggestions come from the embedded frequency vocabulary,
// indexed by length and checked with a bounded Damerau-Levenshtein pass. That
// avoids materializing millions of second-order edit strings for one typo.

const (
	suggestAlphabet         = "abcdefghijklmnopqrstuvwxyz"
	indexedSuggestionRadius = 2
	// Prevent an accidental book-length "word" from making even the linear
	// one-edit generator consume unbounded memory. The embedded vocabulary's
	// longest entry is well below this fiction-friendly ceiling.
	maxSuggestionWordLength = 64
)

// Suggest returns up to maxReplacements corrections, best first.
//
// Edit distance 2 is attempted only when distance 1 yields nothing. Its search
// space is the fixed 50k-word embedded vocabulary rather than edits-of-edits.
func (d *Dictionary) Suggest(word string) []string {
	if word == "" {
		return nil
	}
	lower := strings.ToLower(normalizeApostrophes(word))
	if len(lower) > maxSuggestionWordLength {
		return nil
	}

	cands := d.knownEdits(edits1(lower))
	if len(cands) == 0 {
		cands = d.indexedKnownEdits(lower, indexedSuggestionRadius)
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

// indexedKnownEdits finds distance-two candidates without generating the
// distance-two string universe. The embedded frequency list is already the
// ranking corpus, so it doubles as a compact suggestion vocabulary. Length
// buckets exclude impossible candidates up front; the remaining scan is fixed
// at at most 50k words and each comparison is capped by the 64-byte input bound.
//
// Only the best few same-initial and changed-initial candidates are retained.
// That is sufficient because rankCandidates puts same-initial words first and
// Suggest returns at most maxReplacements results. A candidate still has to
// pass gospell membership, preserving the old dictionary-accepted contract.
func (d *Dictionary) indexedKnownEdits(word string, maxDistance int) []string {
	if maxDistance < 1 || len(d.suggestionWords) == 0 {
		return nil
	}

	minLen := len(word) - maxDistance
	if minLen < 1 {
		minLen = 1
	}
	maxLen := len(word) + maxDistance
	rows := newEditDistanceRows(maxLen + 1)
	var sameHead, otherHead []string

	for length := minLen; length <= maxLen; length++ {
		for _, candidate := range d.suggestionWords[length] {
			if candidate == word || !rows.within(word, candidate, maxDistance) {
				continue
			}
			if len(word) > 0 && candidate[0] == word[0] {
				sameHead = d.keepBestKnown(sameHead, candidate)
			} else {
				otherHead = d.keepBestKnown(otherHead, candidate)
			}
		}
	}

	if len(sameHead) >= maxReplacements {
		return sameHead
	}
	return append(sameHead, otherHead...)
}

// keepBestKnown retains the maxReplacements highest-frequency candidates.
// Ranking is checked before the serialized gospell lookup, so a low-ranked
// match cannot create lock contention once the bounded list is full.
func (d *Dictionary) keepBestKnown(best []string, candidate string) []string {
	candidateRank, ranked := d.rankOf(candidate)
	if !ranked {
		return best
	}

	worst := -1
	worstRank := -1
	for i, current := range best {
		rank, _ := d.rankOf(current)
		if rank > worstRank {
			worst, worstRank = i, rank
		}
	}
	if len(best) >= maxReplacements && candidateRank >= worstRank {
		return best
	}
	if !d.spell(candidate) {
		return best
	}
	if len(best) < maxReplacements {
		return append(best, candidate)
	}
	best[worst] = candidate
	return best
}

// editDistanceRows is reusable scratch for an optimal-string-alignment
// Damerau-Levenshtein check. Adjacent transpositions count as one edit, matching
// edits1's behavior. Reusing three rows avoids an allocation per vocabulary
// word during the indexed fallback.
type editDistanceRows struct {
	twoBack  []int
	previous []int
	current  []int
}

func newEditDistanceRows(width int) *editDistanceRows {
	return &editDistanceRows{
		twoBack:  make([]int, width),
		previous: make([]int, width),
		current:  make([]int, width),
	}
}

func (r *editDistanceRows) within(a, b string, maxDistance int) bool {
	if absInt(len(a)-len(b)) > maxDistance {
		return false
	}
	if len(a) == 0 {
		return len(b) <= maxDistance
	}
	if len(b) == 0 {
		return len(a) <= maxDistance
	}

	width := len(b) + 1
	if len(r.previous) < width {
		*r = *newEditDistanceRows(width)
	}
	for j := 0; j < width; j++ {
		r.previous[j] = j
	}

	for i := 1; i <= len(a); i++ {
		r.current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			value := min3(
				r.previous[j]+1,
				r.current[j-1]+1,
				r.previous[j-1]+cost,
			)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				if transposed := r.twoBack[j-2] + 1; transposed < value {
					value = transposed
				}
			}
			r.current[j] = value
		}
		r.twoBack, r.previous, r.current = r.previous, r.current, r.twoBack
	}
	return r.previous[len(b)] <= maxDistance
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
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
