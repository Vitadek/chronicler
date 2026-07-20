package grammar

import (
	"strings"
)

// Common-case grammar rules.
//
// This is a closed, deliberately small set. LanguageTool carries thousands of
// rules built over many years; matching that is not the goal. The goal is the
// handful of errors that actually survive into a manuscript — agreement slips,
// article errors, doubled words — caught with near-zero false positives.
//
// Anything needing real parsing (clause structure, long-distance agreement) is
// out of scope: without a parser those rules misfire on dialogue and fragments,
// which fiction is made of.

// thirdPersonSingular subjects that require "doesn't"/"wasn't"/"isn't".
var thirdPersonSubjects = set("he", "she", "it", "who", "somebody", "someone",
	"nobody", "everybody", "everyone", "anybody", "anyone", "this", "that")

// pluralSubjects require "don't"/"weren't"/"aren't".
var pluralSubjects = set("they", "we", "you", "these", "those", "people")

// agreementFix maps a contraction to its third-person-singular form.
var agreementFix = map[string]string{
	"don't":   "doesn't",
	"doesnt":  "doesn't",
	"dont":    "doesn't",
	"were":    "was",
	"weren't": "wasn't",
	"are":     "is",
	"aren't":  "isn't",
	"have":    "has",
	"haven't": "hasn't",
}

// pluralFix is the mirror: singular verb after a plural subject.
var pluralFix = map[string]string{
	"doesn't": "don't",
	"was":     "were",
	"wasn't":  "weren't",
	"is":      "are",
	"isn't":   "aren't",
	"has":     "have",
	"hasn't":  "haven't",
}

// checkGrammar returns KindGrammar hits.
func (d *Dictionary) checkGrammar(toks []token, claimed map[int]bool) []Hit {
	var hits []Hit

	for i, t := range toks {
		if claimed[t.Start] {
			continue
		}
		word := strings.ToLower(normalizeApostrophes(t.Word))

		// --- Subject/verb agreement -------------------------------------
		if i > 0 {
			prev := strings.ToLower(normalizeApostrophes(toks[i-1].Word))
			if thirdPersonSubjects[prev] {
				if fix, ok := agreementFix[word]; ok {
					hits = append(hits, Hit{
						Start: t.Start, End: t.End, Kind: KindGrammar,
						Message: "The pronoun “" + toks[i-1].Word + "” usually takes a third-person verb form.",
						Replacements: []string{matchCase(t.Word, fix)},
					})
					claimed[t.Start] = true
					continue
				}
			}
			if pluralSubjects[prev] {
				if fix, ok := pluralFix[word]; ok {
					hits = append(hits, Hit{
						Start: t.Start, End: t.End, Kind: KindGrammar,
						Message: "The subject “" + toks[i-1].Word + "” usually takes a plural verb form.",
						Replacements: []string{matchCase(t.Word, fix)},
					})
					claimed[t.Start] = true
					continue
				}
			}
		}

		// --- a / an -----------------------------------------------------
		if (word == "a" || word == "an") && i+1 < len(toks) {
			next := strings.ToLower(normalizeApostrophes(toks[i+1].Word))
			if next != "" {
				wantAn := startsWithVowelSound(next)
				if word == "a" && wantAn {
					hits = append(hits, Hit{
						Start: t.Start, End: t.End, Kind: KindGrammar,
						Message: "Use “an” before a word beginning with a vowel sound.",
						Replacements: []string{matchCase(t.Word, "an")},
					})
					claimed[t.Start] = true
					continue
				}
				if word == "an" && !wantAn {
					hits = append(hits, Hit{
						Start: t.Start, End: t.End, Kind: KindGrammar,
						Message: "Use “a” before a word beginning with a consonant sound.",
						Replacements: []string{matchCase(t.Word, "a")},
					})
					claimed[t.Start] = true
					continue
				}
			}
		}

		// --- Doubled word ------------------------------------------------
		// "the the". Skipped for words that legitimately repeat ("had had",
		// "that that") and for any repeat spanning a sentence boundary.
		if i > 0 && !claimed[toks[i-1].Start] {
			prev := strings.ToLower(normalizeApostrophes(toks[i-1].Word))
			if prev == word && !legitimateDouble[word] && toks[i-1].End == t.Start-1 {
				hits = append(hits, Hit{
					Start: toks[i-1].Start, End: t.End, Kind: KindGrammar,
					Message: "“" + t.Word + "” is repeated.",
					Replacements: []string{t.Word},
				})
				claimed[t.Start] = true
				continue
			}
		}
	}
	return hits
}

// legitimateDouble lists words that can correctly appear twice in a row.
var legitimateDouble = set("had", "that", "who", "what", "no", "very", "so", "long")

// startsWithVowelSound decides between "a" and "an" — by sound, not spelling.
//
// The exception lists are what make this usable: "an hour" and "a university"
// are both correct, and a naive first-letter test gets both wrong.
func startsWithVowelSound(word string) bool {
	if word == "" {
		return false
	}
	lower := strings.ToLower(word)

	// Silent h: "an hour", "an honest man".
	for _, p := range []string{"hour", "honest", "honor", "honour", "heir", "herb"} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	// Consonantal onset despite a vowel letter: "a university", "a one-time".
	for _, p := range []string{"uni", "use", "user", "usual", "utili", "eu", "ewe", "one", "once"} {
		if strings.HasPrefix(lower, p) {
			return false
		}
	}
	switch lower[0] {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}
