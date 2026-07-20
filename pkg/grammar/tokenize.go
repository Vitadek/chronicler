package grammar

import (
	"strings"
	"unicode"
)

// token is one word occurrence in the submitted text.
type token struct {
	// Word as it appeared, original case.
	Word string
	// Offsets in UTF-16 code units — see the Hit doc comment for why.
	Start int
	End   int
}

// tokenize splits text into word tokens, carrying UTF-16 offsets.
//
// A "word" is a run of letters plus internal apostrophes and hyphens, so
// "don't", "rock-hard" and the curly-apostrophe "don’t" survive as single
// tokens — important because both the confusion table and the grammar rules
// match on contractions.
//
// Regions that must never be linted are skipped wholesale but still advance the
// offset counter, so hits keep indexing the original string:
//   - URLs and bare domains (a misspelt-looking host is not a typo)
//   - anything inside backticks (inline code)
func tokenize(text string) []token {
	var out []token

	runes := []rune(text)
	// u16At[i] is the UTF-16 offset of runes[i]; one extra entry holds the
	// total length so End offsets past the last rune are addressable.
	u16At := make([]int, len(runes)+1)
	u16 := 0
	for i, r := range runes {
		u16At[i] = u16
		if r > 0xFFFF {
			u16 += 2 // astral plane: surrogate pair in UTF-16
		} else {
			u16++
		}
	}
	u16At[len(runes)] = u16

	inCode := false
	i := 0
	for i < len(runes) {
		r := runes[i]

		if r == '`' {
			inCode = !inCode
			i++
			continue
		}
		if inCode {
			i++
			continue
		}

		if !isWordRune(r) {
			i++
			continue
		}

		start := i
		for i < len(runes) && isWordRune(runes[i]) {
			i++
		}
		// Trailing connectors ("word-", "don'") belong to punctuation.
		for i > start && isConnector(runes[i-1]) {
			i--
		}

		word := string(runes[start:i])
		if word == "" {
			i = start + 1
			continue
		}
		if skipLikeURL(runes, start, i) {
			// Consume the rest of the URL/domain so its path segments aren't
			// linted as words either.
			for i < len(runes) && !unicode.IsSpace(runes[i]) {
				i++
			}
			continue
		}
		out = append(out, token{Word: word, Start: u16At[start], End: u16At[i]})
	}
	return out
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || isConnector(r)
}

// isConnector covers both the ASCII apostrophe and U+2019, which Chronicle's
// Typography extension substitutes in as the user types.
func isConnector(r rune) bool {
	return r == '\'' || r == '’' || r == '-'
}

// skipLikeURL reports whether the word at [start,end) begins a URL or bare
// domain. Cheap prefix/suffix heuristics — this only has to be good enough to
// stop "github" or "myapp" from being reported as misspellings.
func skipLikeURL(runes []rune, start, end int) bool {
	word := strings.ToLower(string(runes[start:end]))
	switch word {
	case "http", "https", "ftp", "mailto", "www":
		// Followed by "://" or "." — otherwise it's the ordinary English word.
		if end < len(runes) && (runes[end] == ':' || runes[end] == '.') {
			return true
		}
	}
	// A dot immediately followed by a known TLD ("example.com").
	if end+1 < len(runes) && runes[end] == '.' {
		rest := string(runes[end+1:])
		for _, tld := range []string{"com", "org", "net", "io", "dev", "gov", "edu", "co"} {
			if strings.HasPrefix(strings.ToLower(rest), tld) {
				return true
			}
		}
	}
	return false
}

// wordsOnly strips connectors for dictionary lookups: gospell knows "don't"
// but not "don’t" with a curly apostrophe.
func normalizeApostrophes(word string) string {
	return strings.ReplaceAll(word, "’", "'")
}
