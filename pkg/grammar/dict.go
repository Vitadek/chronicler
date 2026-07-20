package grammar

import (
	"bufio"
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"sync"

	"github.com/client9/gospell"
)

// The dictionary and frequency list are compiled into the binary so a
// Chronicle server has a working spell checker with nothing installed and no
// network. See assets/dict/LICENSE.txt for provenance and terms.

//go:embed assets/en_US.aff
var affBytesUS []byte

//go:embed assets/en_US.dic
var dicBytesUS []byte

//go:embed assets/en_GB.aff
var affBytesGB []byte

//go:embed assets/en_GB.dic
var dicBytesGB []byte

//go:embed assets/frequency.txt
var frequencyBytes []byte

// Dictionary is the loaded spelling data: Hunspell for membership, a frequency
// ranking for ordering suggestions.
type Dictionary struct {
	// spellers are consulted in order; a word known to ANY of them is correct.
	//
	// Both US and GB are loaded because Chronicle must not impose a dialect on
	// a novelist. With US alone, "grey", "colour", "realise", "theatre" and
	// every -ise/-our/-re form squiggle red — a false-positive class that
	// showed up immediately on ordinary prose. Loading the GB dictionary
	// rather than hand-listing variants means the affix rules expand
	// inflections ("realised", "colouring") correctly too.
	spellers []*gospell.GoSpell
	// rank maps a lowercase word to its corpus rank (0 = most frequent).
	// Only used to order candidates that already passed a speller.
	rank map[string]int
}

var (
	loadOnce sync.Once
	shared   *Dictionary
	loadErr  error
)

// Load parses the embedded dictionary once and returns the shared instance.
//
// Parsing the .aff affix rules and expanding the ~49.5k .dic stems is the
// expensive part (tens of ms), so it happens exactly once per process; the
// result is read-only and safe for concurrent use.
func Load() (*Dictionary, error) {
	loadOnce.Do(func() {
		var spellers []*gospell.GoSpell
		for _, d := range []struct {
			name string
			aff  []byte
			dic  []byte
		}{
			{"en_US", affBytesUS, dicBytesUS},
			{"en_GB", affBytesGB, dicBytesGB},
		} {
			speller, err := gospell.NewGoSpellReader(
				bytes.NewReader(d.aff),
				bytes.NewReader(d.dic),
			)
			if err != nil {
				loadErr = fmt.Errorf("grammar: parse embedded %s dictionary: %w", d.name, err)
				return
			}
			spellers = append(spellers, speller)
		}

		rank := make(map[string]int, 50_000)
		scanner := bufio.NewScanner(bytes.NewReader(frequencyBytes))
		for i := 0; scanner.Scan(); {
			word := strings.TrimSpace(scanner.Text())
			if word == "" {
				continue
			}
			// First occurrence wins: the file is ordered most-frequent-first.
			if _, seen := rank[word]; !seen {
				rank[word] = i
			}
			i++
		}
		if err := scanner.Err(); err != nil {
			loadErr = fmt.Errorf("grammar: read embedded frequency list: %w", err)
			return
		}

		shared = &Dictionary{spellers: spellers, rank: rank}
	})
	return shared, loadErr
}

// spell reports whether any loaded dictionary accepts the exact word.
func (d *Dictionary) spell(word string) bool {
	for _, s := range d.spellers {
		if s.Spell(word) {
			return true
		}
	}
	return false
}

// Known reports whether the word is spelled correctly.
//
// Case handling mirrors what writers expect: a capitalized word is accepted if
// either the exact form or its lowercase form is in the dictionary, so
// sentence-initial "The" and the proper noun "Mark" both pass without the
// dictionary needing every capitalization.
func (d *Dictionary) Known(word string) bool {
	w := normalizeApostrophes(word)
	if w == "" {
		return true
	}
	if d.spell(w) {
		return true
	}
	lower := strings.ToLower(w)
	if lower != w && d.spell(lower) {
		return true
	}
	// "DOOR" / "DON'T" — all-caps emphasis is common in prose.
	if w == strings.ToUpper(w) {
		if title := strings.ToUpper(w[:1]) + strings.ToLower(w[1:]); d.spell(title) {
			return true
		}
	}
	return false
}

// rankOf returns the corpus rank of a word, and whether it was ranked at all.
func (d *Dictionary) rankOf(word string) (int, bool) {
	r, ok := d.rank[strings.ToLower(word)]
	return r, ok
}
