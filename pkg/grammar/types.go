// Package grammar is Chronicler's built-in prose checker.
//
// It replaces the Java LanguageTool sidecar the Node server proxied to. Every
// check runs in-process against embedded data: no sidecar, no network, no API
// key, and no cgo (consistent with the modernc.org/sqlite choice elsewhere).
//
// # Wire contract
//
// The output of Check is served verbatim by POST /api/grammar/check and must
// stay byte-compatible with the Node route it replaces
// (chronicle/server/routes/grammar.ts), because the whole frontend is built on
// its shape and — more subtly — on its `kind` vocabulary:
//
//   - "misspelling" draws a red squiggle, is filterable by the user's custom
//     dictionary (chronicle/src/lib/dictionary.ts), and feeds ProofreadView's
//     Spelling tab.
//   - "confusion" is deliberately NOT "misspelling". Confused pairs are
//     correctly spelled words; classifying them as misspellings would invite
//     "add to dictionary", which would whitelist a common English word and
//     silence the rule everywhere. It also drives the proofreader plugin's
//     Word-choice pane, which filters on exactly this string.
//   - "grammar" draws a red squiggle (see ERROR_KINDS in
//     chronicle/src/lib/Grammar.ts).
//   - "style" is advisory and renders blue.
//
package grammar

// Hit kinds. These strings are a contract with the frontend — see the package
// comment before changing or adding any.
const (
	KindMisspelling = "misspelling"
	KindConfusion   = "confusion"
	KindGrammar     = "grammar"
	KindStyle       = "style"
)

// maxReplacements caps suggestions per hit, matching the Node route's
// `.slice(0, 5)` — LanguageTool could return dozens for a bad typo.
const maxReplacements = 5

// Hit is one finding.
//
// Start and End are offsets into the SUBMITTED text, measured in UTF-16 code
// units — not bytes and not runes. That is what the client indexes with
// (chronicle/src/lib/proseMirrorText.ts walks `t[k]` for k < t.length over a
// JavaScript string), and it matters in practice because Chronicler's smart
// typography emits curly quotes and em dashes, which are multi-byte in UTF-8.
type Hit struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Kind  string `json:"kind"`
	Message string `json:"message"`
	// Always serialized, never null: the Node route emitted `[]` when there
	// were no candidates and ProofreadView indexes it without a guard.
	Replacements []string `json:"replacements"`
}
