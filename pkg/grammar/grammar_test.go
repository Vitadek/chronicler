package grammar

import (
	"strings"
	"testing"
)

// The assertions here are pinned to a live LanguageTool baseline captured from
// the running Node deployment before it was replaced, so this suite documents
// what the native checkers must reproduce — and, just as importantly, the
// false positives they must not introduce.

func load(t *testing.T) *Dictionary {
	t.Helper()
	d, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	return d
}

func hitsFor(t *testing.T, text, kind string) []Hit {
	t.Helper()
	var out []Hit
	for _, h := range load(t).Check(text) {
		if h.Kind == kind {
			out = append(out, h)
		}
	}
	return out
}

func hasReplacement(h Hit, want string) bool {
	for _, r := range h.Replacements {
		if strings.EqualFold(r, want) {
			return true
		}
	}
	return false
}

// --- Spelling ---------------------------------------------------------------

// LanguageTool returned "quikly" -> ["quickly"] and "teh" -> ["the","ten",
// "tea","tech","Ted"]. Matching the top suggestion is the bar; matching the
// whole list is not, since ranking differs by corpus.
func TestSpelling_MatchesLanguageToolBaseline(t *testing.T) {
	cases := []struct{ text, word, want string }{
		{"She quikly walked to the door.", "quikly", "quickly"},
		{"She walked to teh door.", "teh", "the"},
		{"He recieved the letter.", "recieved", "received"},
	}
	for _, tc := range cases {
		hits := hitsFor(t, tc.text, KindMisspelling)
		if len(hits) == 0 {
			t.Errorf("%q: expected a misspelling for %q, got none", tc.text, tc.word)
			continue
		}
		if !hasReplacement(hits[0], tc.want) {
			t.Errorf("%q: expected %q among replacements, got %v",
				tc.text, tc.want, hits[0].Replacements)
		}
	}
}

func TestSpelling_OffsetsIndexTheSubmittedString(t *testing.T) {
	text := "She quikly walked."
	hits := hitsFor(t, text, KindMisspelling)
	if len(hits) != 1 {
		t.Fatalf("expected 1 misspelling, got %d", len(hits))
	}
	// Offsets are UTF-16 units; this input is ASCII so they equal byte offsets.
	if got := text[hits[0].Start:hits[0].End]; got != "quikly" {
		t.Errorf("offsets select %q, want %q", got, "quikly")
	}
}

// Chronicle's Typography extension emits curly quotes and em dashes, so a hit
// after one must still index correctly on the client, which counts UTF-16
// units rather than bytes.
func TestSpelling_OffsetsSurviveNonASCII(t *testing.T) {
	text := "“Hello,” she said — the sky was quikly darkening."
	hits := hitsFor(t, text, KindMisspelling)
	if len(hits) != 1 {
		t.Fatalf("expected 1 misspelling, got %d: %+v", len(hits), hits)
	}
	runes := []rune(text)
	if got := string(runes[hits[0].Start:hits[0].End]); got != "quikly" {
		t.Errorf("offsets select %q, want %q", got, "quikly")
	}
}

func TestSpelling_IgnoresInventedProperNouns(t *testing.T) {
	// The single largest false-positive source in fiction.
	text := "Katherine crossed the bridge into Vexhollow."
	if hits := hitsFor(t, text, KindMisspelling); len(hits) != 0 {
		t.Errorf("invented names should not be flagged, got %+v", hits)
	}
}

// Chronicle must not impose a dialect on a novelist. Caught in review: with
// only en_US loaded, "grey" squiggled red in otherwise clean prose, and every
// -ise/-our/-re form with it. Both dictionaries are loaded, so the union is
// accepted — including inflections, which is why the GB dictionary is loaded
// rather than a hand-written variant list.
func TestSpelling_AcceptsBothDialects(t *testing.T) {
	british := []string{
		"grey", "colour", "honour", "realise", "recognise", "organise",
		"theatre", "centre", "defence", "travelled", "favourite", "neighbour",
		"apologise", "labour", "armour", "behaviour",
		// Inflections — these only work because affixes expand.
		"realised", "colouring", "travelling", "apologised",
	}
	american := []string{
		"gray", "color", "honor", "realize", "recognize", "organize",
		"theater", "center", "defense", "traveled", "favorite", "neighbor",
	}
	d := load(t)
	for _, w := range append(british, american...) {
		if !d.Known(w) {
			t.Errorf("%q should be accepted (dialect variant), but was flagged", w)
		}
	}
}

func TestSpelling_IgnoresURLs(t *testing.T) {
	text := "See https://github.com/errata-ai/vale for details."
	if hits := hitsFor(t, text, KindMisspelling); len(hits) != 0 {
		t.Errorf("URLs should not be flagged, got %+v", hits)
	}
}

// --- Confusion --------------------------------------------------------------

// The critical classification test. LanguageTool reported "quiet"/"quite" with
// issueType "misspelling", which the Node route deliberately re-labelled: a
// misspelling can be silenced via "add to dictionary", which would whitelist a
// real English word everywhere. It must be "confusion".
func TestConfusion_IsNotClassifiedAsMisspelling(t *testing.T) {
	text := "The weather was quiet nice yesterday."

	conf := hitsFor(t, text, KindConfusion)
	if len(conf) != 1 {
		t.Fatalf("expected 1 confusion hit, got %d", len(conf))
	}
	if !hasReplacement(conf[0], "quite") {
		t.Errorf("expected “quite” suggested, got %v", conf[0].Replacements)
	}
	if got := text[conf[0].Start:conf[0].End]; got != "quiet" {
		t.Errorf("hit spans %q, want %q", got, "quiet")
	}

	if mis := hitsFor(t, text, KindMisspelling); len(mis) != 0 {
		t.Errorf("a correctly spelled confused word must not also be a misspelling, got %+v", mis)
	}
}

func TestConfusion_RequiresContext(t *testing.T) {
	// Correct uses must stay silent — an unconditional rule would fire here.
	for _, text := range []string{
		"The room was quiet.",
		"Their house is red.",
		"Put it over there.",
		"Your book is on the table.",
		"The dog wagged its tail.",
	} {
		if hits := hitsFor(t, text, KindConfusion); len(hits) != 0 {
			t.Errorf("%q: expected no confusion hit, got %+v", text, hits)
		}
	}
}

func TestConfusion_TheirVersusTheyre(t *testing.T) {
	hits := hitsFor(t, "Their going to the store later.", KindConfusion)
	if len(hits) != 1 {
		t.Fatalf("expected 1 confusion hit, got %d", len(hits))
	}
	if !hasReplacement(hits[0], "they're") {
		t.Errorf("expected “they’re”, got %v", hits[0].Replacements)
	}
}

// --- Grammar ----------------------------------------------------------------

func TestGrammar_SubjectVerbAgreement(t *testing.T) {
	// LanguageTool: "The pronoun 'He' is usually used with a third-person or a
	// past tense verb", suggesting does/did.
	hits := hitsFor(t, "He don't know what happened.", KindGrammar)
	if len(hits) != 1 {
		t.Fatalf("expected 1 grammar hit, got %d", len(hits))
	}
	if !hasReplacement(hits[0], "doesn't") {
		t.Errorf("expected “doesn't”, got %v", hits[0].Replacements)
	}
}

func TestGrammar_ArticleAgreement(t *testing.T) {
	cases := []struct {
		text     string
		wantHits int
		want     string
	}{
		{"She waited a hour for the train.", 1, "an"},
		{"He gave an report to the captain.", 1, "a"},
		// Sound, not spelling — both of these are already correct.
		{"She waited an hour for the train.", 0, ""},
		{"He attended a university in the north.", 0, ""},
	}
	for _, tc := range cases {
		hits := hitsFor(t, tc.text, KindGrammar)
		if len(hits) != tc.wantHits {
			t.Errorf("%q: expected %d grammar hits, got %d (%+v)",
				tc.text, tc.wantHits, len(hits), hits)
			continue
		}
		if tc.wantHits > 0 && !hasReplacement(hits[0], tc.want) {
			t.Errorf("%q: expected %q, got %v", tc.text, tc.want, hits[0].Replacements)
		}
	}
}

func TestGrammar_DoubledWord(t *testing.T) {
	if hits := hitsFor(t, "She opened the the door.", KindGrammar); len(hits) != 1 {
		t.Errorf("expected 1 doubled-word hit, got %d (%+v)", len(hits), hits)
	}
	// "had had" is correct English and must not fire.
	if hits := hitsFor(t, "He had had enough by then.", KindGrammar); len(hits) != 0 {
		t.Errorf("“had had” should not be flagged, got %+v", hits)
	}
}

// --- Style ------------------------------------------------------------------

// The one gap the LanguageTool comparison surfaced: LT flagged "very very"
// (suggesting a comma). Advisory rather than an error, so it must be KindStyle.
func TestStyle_RepeatedIntensifier(t *testing.T) {
	text := "She was very very tired."
	hits := hitsFor(t, text, KindStyle)
	if len(hits) != 1 {
		t.Fatalf("expected 1 style hit, got %d (%+v)", len(hits), hits)
	}
	if got := text[hits[0].Start:hits[0].End]; got != "very very" {
		t.Errorf("hit spans %q, want %q", got, "very very")
	}
	// Must not be an error kind — ERROR_KINDS in the frontend draws those red.
	for _, k := range []string{KindMisspelling, KindGrammar} {
		if h := hitsFor(t, text, k); len(h) != 0 {
			t.Errorf("repeated intensifier must not be %s, got %+v", k, h)
		}
	}
}

func TestStyle_FilterWords(t *testing.T) {
	if hits := hitsFor(t, "She saw the door swing open.", KindStyle); len(hits) != 1 {
		t.Errorf("expected a filter-word hit, got %d (%+v)", len(hits), hits)
	}
	// No viewpoint subject — deliberately not flagged.
	if hits := hitsFor(t, "The guards saw the door swing open.", KindStyle); len(hits) != 0 {
		t.Errorf("expected no filter-word hit without a viewpoint subject, got %+v", hits)
	}
}

// --- False positives on real prose -----------------------------------------

// The regression that made stock Vale unusable: its write-good/proselint
// packages produced 8 alerts on this passage, 7 of them "avoid using 'was'".
// Clean fiction must produce no errors at all.
func TestCleanFictionProse_ProducesNoErrors(t *testing.T) {
	// Deliberately includes "grey" — the British spelling that exposed the
	// dialect gap when only en_US was loaded.
	const prose = `The rain was falling when Katherine stepped onto the platform. ` +
		`She had been waiting for three hours, and there was no sign of the train. ` +
		`It was the kind of silence that felt deliberate, as though the station ` +
		`itself was holding its breath. He looked at her with those grey eyes ` +
		`that had always been impossible to read.`

	for _, kind := range []string{KindMisspelling, KindGrammar, KindConfusion} {
		if hits := hitsFor(t, prose, kind); len(hits) != 0 {
			t.Errorf("clean prose produced %s hits: %+v", kind, hits)
		}
	}
}

// --- Contract ---------------------------------------------------------------

func TestCheck_NeverReturnsNil(t *testing.T) {
	// The route serializes this directly; `{"hits": null}` would break the
	// client's `data.hits || []` narrowing.
	for _, text := range []string{"", "   ", "Perfectly ordinary prose."} {
		if got := load(t).Check(text); got == nil {
			t.Errorf("Check(%q) returned nil, want empty slice", text)
		}
	}
}

func TestCheck_ReplacementsNeverNil(t *testing.T) {
	// ProofreadView indexes `replacements` without a guard.
	for _, h := range load(t).Check("He don't know, and she saw teh door.") {
		if h.Replacements == nil {
			t.Errorf("hit %+v has nil Replacements; want empty slice", h)
		}
	}
}

func TestCheck_ReplacementsAreCapped(t *testing.T) {
	for _, h := range load(t).Check("She quikly opened teh door and recieved teh letter.") {
		if len(h.Replacements) > maxReplacements {
			t.Errorf("hit %+v exceeds the %d-replacement cap", h, maxReplacements)
		}
	}
}

func TestCheck_HitsAreSortedByPosition(t *testing.T) {
	hits := load(t).Check("Their going to teh store, and he don't know why.")
	for i := 1; i < len(hits); i++ {
		if hits[i].Start < hits[i-1].Start {
			t.Errorf("hits out of order at %d: %+v", i, hits)
			break
		}
	}
}
