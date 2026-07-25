package languagetool

import (
	"strings"

	"chronicle-server/pkg/grammar"
)

// kindFor starts from the Node server's classification
// (chronicle/server/routes/grammar.ts:37-41) so LT hits share chronicler's
// existing kind vocabulary (pkg/grammar/types.go) and the frontend needs no
// per-engine branching, then extends it with a second confusion signal found
// by live-testing against a real LanguageTool 6.8 + n-gram model:
//
// Two DIFFERENT LT mechanisms produce confused-word hits, with different
// metadata:
//   - Pattern-based rules (grammar.xml) tag category.id "CONFUSED_WORDS".
//   - The statistical ConfusionProbabilityRule — the one n-grams actually
//     activate, and the one that fires for nearly every pair in
//     confusion_sets.txt (quiet/quite, accept/except, their/there, ...) —
//     instead tags category.id "TYPOS" and issueType "non-conformance", but
//     always with rule.id prefixed "CONFUSION_RULE_". Missing this second
//     signal means every n-gram-scored confusion hit falls through to
//     kind="non-conformance", which isn't in the frontend's kind vocabulary
//     at all (renders as a style hit and never reaches the Word-choice
//     bucket) — confirmed by hand against a live sidecar before this fix.
//
// Both mechanisms flag correctly SPELLED words (quiet, their, accept are all
// real words), so both must map to "confusion", never "misspelling": that
// would invite "add to dictionary", whitelisting a common English word and
// silencing the rule everywhere.
func kindFor(m ltMatch) string {
	if m.Rule.Category.Id == "CONFUSED_WORDS" || strings.HasPrefix(m.Rule.Id, "CONFUSION_RULE_") {
		return grammar.KindConfusion
	}
	if m.Rule.IssueType != "" {
		return m.Rule.IssueType
	}
	if m.Rule.Category.Id != "" {
		return m.Rule.Category.Id
	}
	return grammar.KindGrammar
}
