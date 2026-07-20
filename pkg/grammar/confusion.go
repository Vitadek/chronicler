package grammar

import "strings"

// Confused-pair detection: correctly spelled words used in place of one
// another. This is the class LanguageTool reported as CONFUSED_WORDS, and the
// Node route deliberately re-labelled to "confusion" rather than "misspelling"
// — see the package comment for why that distinction is load-bearing.
//
// Every rule here is context-gated. An unconditional "their -> they're" would
// fire on correct prose constantly, and a checker that cries wolf on a
// novelist's own voice gets switched off within a chapter. Where context can't
// be established cheaply and reliably, the pair is simply left out: silence
// beats a false positive.

// confusionRule fires when Word appears next to a qualifying neighbour.
type confusionRule struct {
	// Word is the (lowercase) token that might be wrong.
	Word string
	// Suggest is what to offer instead.
	Suggest string
	// NextIn fires when the FOLLOWING word is in this set.
	NextIn map[string]bool
	// PrevIn fires when the PRECEDING word is in this set.
	PrevIn map[string]bool
	// Message explains the distinction, in LanguageTool's advisory register.
	Message string
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// Common adjectives/adverbs that "quite" modifies but "quiet" cannot.
var intensifiable = set(
	"nice", "good", "well", "large", "small", "big", "long", "short", "old",
	"young", "different", "similar", "clear", "certain", "sure", "possible",
	"likely", "common", "rare", "close", "far", "early", "late", "often",
	"happy", "sad", "angry", "tired", "busy", "quick", "slow", "easy", "hard",
	"pleased", "right", "wrong", "true", "false", "cold", "warm", "hot",
)

// Verb-ish words that follow "they're"/"you're"/"it's" but not the possessives.
var afterContraction = set(
	"going", "coming", "doing", "being", "getting", "making", "taking",
	"looking", "trying", "working", "playing", "running", "walking", "talking",
	"waiting", "standing", "sitting", "moving", "leaving", "staying",
	"probably", "certainly", "definitely", "already", "always", "never",
	"still", "just", "not", "all", "both", "so", "very", "really", "too",
)

var confusionRules = []confusionRule{
	{
		Word: "quiet", Suggest: "quite", NextIn: intensifiable,
		Message: "Did you mean “quite”? ‘quiet’ means ‘silent’; ‘quite’ means ‘very’ or ‘to a moderate extent’.",
	},
	{
		Word: "quite", Suggest: "quiet",
		PrevIn:  set("stayed", "kept", "stay", "keep", "went", "fell", "grew", "remained"),
		Message: "Did you mean “quiet”? ‘quite’ means ‘very’; ‘quiet’ means ‘silent’.",
	},
	{
		Word: "their", Suggest: "they're", NextIn: afterContraction,
		Message: "Did you mean “they’re” (they are)? ‘their’ is possessive.",
	},
	{
		Word: "there", Suggest: "they're", NextIn: afterContraction,
		Message: "Did you mean “they’re” (they are)? ‘there’ refers to a place.",
	},
	{
		Word: "your", Suggest: "you're", NextIn: afterContraction,
		Message: "Did you mean “you’re” (you are)? ‘your’ is possessive.",
	},
	{
		Word: "its", Suggest: "it's", NextIn: afterContraction,
		Message: "Did you mean “it’s” (it is)? ‘its’ is possessive.",
	},
	{
		// "he lead the group" — past tense wants "led". Present-tense "they
		// lead" is correct, so only third-person-singular subjects fire.
		Word: "lead", Suggest: "led",
		PrevIn:  set("he", "she", "it", "who", "that", "and"),
		Message: "Did you mean “led”? ‘lead’ is the present tense or the metal; ‘led’ is the past tense.",
	},
	{
		Word: "then", Suggest: "than",
		PrevIn:  set("more", "less", "better", "worse", "rather", "other", "greater", "fewer", "larger", "smaller"),
		Message: "Did you mean “than”? ‘then’ is about time; ‘than’ is for comparisons.",
	},
	{
		Word: "affect", Suggest: "effect",
		PrevIn:  set("the", "an", "a", "this", "that", "its", "his", "her", "their", "no", "side"),
		Message: "Did you mean “effect”? ‘affect’ is usually the verb; ‘effect’ is usually the noun.",
	},
	{
		Word: "loose", Suggest: "lose",
		PrevIn:  set("to", "will", "would", "could", "might", "must", "may", "cannot", "don't", "didn't"),
		Message: "Did you mean “lose”? ‘loose’ is the opposite of tight; ‘lose’ is the opposite of win or find.",
	},
	{
		Word: "hear", Suggest: "here",
		PrevIn:  set("over", "in", "out", "right", "come", "from", "up"),
		Message: "Did you mean “here”? ‘hear’ is what ears do; ‘here’ is this place.",
	},
}

// checkConfusion returns confusion hits and records which tokens it claimed,
// so the spell checker doesn't also flag them.
func (d *Dictionary) checkConfusion(toks []token) ([]Hit, map[int]bool) {
	var hits []Hit
	claimed := make(map[int]bool)

	for i, t := range toks {
		word := strings.ToLower(normalizeApostrophes(t.Word))
		for _, rule := range confusionRules {
			if rule.Word != word {
				continue
			}
			if !confusionApplies(rule, toks, i) {
				continue
			}
			hits = append(hits, Hit{
				Start:        t.Start,
				End:          t.End,
				Kind:         KindConfusion,
				Message:      rule.Message,
				Replacements: []string{matchCase(t.Word, rule.Suggest)},
			})
			claimed[t.Start] = true
			break // one rule per token
		}
	}
	return hits, claimed
}

func confusionApplies(rule confusionRule, toks []token, i int) bool {
	if rule.NextIn != nil {
		if i+1 >= len(toks) {
			return false
		}
		if !rule.NextIn[strings.ToLower(normalizeApostrophes(toks[i+1].Word))] {
			return false
		}
	}
	if rule.PrevIn != nil {
		if i == 0 {
			return false
		}
		if !rule.PrevIn[strings.ToLower(normalizeApostrophes(toks[i-1].Word))] {
			return false
		}
	}
	// A rule with no context at all would fire on every occurrence.
	return rule.NextIn != nil || rule.PrevIn != nil
}
