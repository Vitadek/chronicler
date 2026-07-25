package languagetool

import (
	"testing"

	"chronicle-server/pkg/grammar"
)

func TestKindFor_ConfusedWordsIsConfusion(t *testing.T) {
	m := ltMatch{}
	m.Rule.IssueType = "misspelling"
	m.Rule.Category.Id = "CONFUSED_WORDS"

	got := kindFor(m)
	if got != grammar.KindConfusion {
		t.Errorf("kindFor(CONFUSED_WORDS) = %q, want %q", got, grammar.KindConfusion)
	}
}

// Pinned against a live LanguageTool 6.8 response for "I saw their yesterday
// at the park." with the n-gram model loaded: rule.id
// "CONFUSION_RULE_THEIR_THERE", category.id "TYPOS", issueType
// "non-conformance" — category alone (the other branch) does NOT catch this.
func TestKindFor_ConfusionRuleIdIsConfusion(t *testing.T) {
	m := ltMatch{}
	m.Rule.Id = "CONFUSION_RULE_THEIR_THERE"
	m.Rule.IssueType = "non-conformance"
	m.Rule.Category.Id = "TYPOS"

	got := kindFor(m)
	if got != grammar.KindConfusion {
		t.Errorf("kindFor(CONFUSION_RULE_*) = %q, want %q", got, grammar.KindConfusion)
	}
}

func TestKindFor_FallsBackToIssueType(t *testing.T) {
	m := ltMatch{}
	m.Rule.IssueType = "style"
	m.Rule.Category.Id = "STYLE"

	got := kindFor(m)
	if got != "style" {
		t.Errorf("kindFor(issueType=style) = %q, want %q", got, "style")
	}
}

func TestKindFor_FallsBackToCategoryWhenNoIssueType(t *testing.T) {
	m := ltMatch{}
	m.Rule.Category.Id = "GRAMMAR"

	got := kindFor(m)
	if got != "GRAMMAR" {
		t.Errorf("kindFor(no issueType) = %q, want %q", got, "GRAMMAR")
	}
}

func TestKindFor_DefaultsToGrammar(t *testing.T) {
	m := ltMatch{}

	got := kindFor(m)
	if got != grammar.KindGrammar {
		t.Errorf("kindFor(empty) = %q, want %q", got, grammar.KindGrammar)
	}
}
