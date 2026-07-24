package languagetool

import "chronicle-server/pkg/grammar"

// kindFor ports the Node server's classification 1:1
// (chronicle/server/routes/grammar.ts:37-41), so LT hits share chronicler's
// existing kind vocabulary (pkg/grammar/types.go) and the frontend needs no
// per-engine branching.
//
// LT reports CONFUSED_WORDS hits (quiet/quite, their/there) with issueType
// "misspelling" even though the flagged word is spelled correctly. Passing
// that through would invite "add to dictionary", which whitelists a common
// English word and silences the rule everywhere — so these get their own
// kind instead.
func kindFor(m ltMatch) string {
	if m.Rule.Category.Id == "CONFUSED_WORDS" {
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
