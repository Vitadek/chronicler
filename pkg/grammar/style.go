package grammar

import "strings"

// Fiction-tuned style advice.
//
// This set exists because the off-the-shelf alternative is actively wrong here.
// Benchmarking Vale's write-good/proselint packages against clean novel prose
// produced 8 alerts, 7 of them "avoid using 'was'" (E-Prime) — advice aimed at
// technical documentation that would flag most of any literary novel. Those
// packages optimize for terse, active, unambiguous instruction; fiction wants
// none of that.
//
// So these rules target things novelists and their editors actually flag, and
// all of them emit KindStyle, which the frontend renders as blue advisory
// rather than a red error (see ERROR_KINDS in chronicle/src/lib/Grammar.ts).

// filterWords distance the reader from the scene: "she saw the door open"
// rather than "the door opened". A staple of line edits.
var filterWords = set(
	"saw", "heard", "felt", "noticed", "realized", "realised", "wondered",
	"thought", "decided", "seemed", "watched", "looked", "knew", "remembered",
)

// filterSubjects — only flag filter words with a clear viewpoint subject, so
// "he saw" fires but "the guards saw" (which may be deliberate) does not.
var filterSubjects = set("he", "she", "i", "they", "we", "you", "it")

// intensifiers that read badly when doubled ("very very tired").
var intensifiers = set("very", "really", "quite", "rather", "pretty", "so",
	"extremely", "incredibly", "totally", "absolutely", "utterly")

// wordy phrases with a tighter equivalent. Kept short and uncontroversial:
// every entry here should be an improvement in essentially any register.
var wordyPhrases = map[string]string{
	"in order to":         "to",
	"due to the fact that": "because",
	"at this point in time": "now",
	"in spite of the fact": "although",
	"for the purpose of":  "to",
	"in the event that":   "if",
	"a large number of":   "many",
	"the majority of":     "most",
	"is able to":          "can",
	"was able to":         "could",
}

// checkStyle returns KindStyle hits.
func (d *Dictionary) checkStyle(text string, toks []token, claimed map[int]bool) []Hit {
	var hits []Hit

	// --- Filter words ---------------------------------------------------
	for i, t := range toks {
		if claimed[t.Start] || i == 0 {
			continue
		}
		if !filterWords[strings.ToLower(normalizeApostrophes(t.Word))] {
			continue
		}
		if !filterSubjects[strings.ToLower(normalizeApostrophes(toks[i-1].Word))] {
			continue
		}
		hits = append(hits, Hit{
			Start: toks[i-1].Start, End: t.End, Kind: KindStyle,
			Message: "Filter word: “" + toks[i-1].Word + " " + t.Word +
				"” puts the viewpoint character between the reader and the scene. Consider showing the action directly.",
			Replacements: []string{},
		})
	}

	// --- Adverb stacking -------------------------------------------------
	// Two -ly adverbs in a row is nearly always one too many.
	for i := 1; i < len(toks); i++ {
		if claimed[toks[i].Start] {
			continue
		}
		if isLyAdverb(toks[i].Word) && isLyAdverb(toks[i-1].Word) {
			hits = append(hits, Hit{
				Start: toks[i-1].Start, End: toks[i].End, Kind: KindStyle,
				Message:      "Two adverbs in a row — consider a stronger verb instead.",
				Replacements: []string{},
			})
		}
	}

	// --- Repeated intensifiers -------------------------------------------
	// "very very tired". LanguageTool flagged this (suggesting a comma); it is
	// advisory rather than an error, because the repetition is sometimes a
	// deliberate voice choice — hence KindStyle, which renders blue.
	for i := 1; i < len(toks); i++ {
		if claimed[toks[i].Start] {
			continue
		}
		w := strings.ToLower(normalizeApostrophes(toks[i].Word))
		if !intensifiers[w] || w != strings.ToLower(normalizeApostrophes(toks[i-1].Word)) {
			continue
		}
		hits = append(hits, Hit{
			Start: toks[i-1].Start, End: toks[i].End, Kind: KindStyle,
			Message: "Repeated intensifier — consider a stronger word, or a comma between them.",
			Replacements: []string{
				toks[i].Word,
				toks[i-1].Word + ", " + toks[i].Word,
			},
		})
	}

	// --- Wordy phrases ----------------------------------------------------
	lower := strings.ToLower(text)
	for phrase, tighter := range wordyPhrases {
		for _, span := range findAllUTF16(lower, text, phrase) {
			hits = append(hits, Hit{
				Start: span[0], End: span[1], Kind: KindStyle,
				Message:      "“" + phrase + "” is wordy — consider “" + tighter + "”.",
				Replacements: []string{tighter},
			})
		}
	}

	return hits
}

func isLyAdverb(word string) bool {
	w := strings.ToLower(word)
	if len(w) < 5 || !strings.HasSuffix(w, "ly") {
		return false
	}
	// "only", "family", "reply" etc. are not adverbs of manner.
	switch w {
	case "only", "family", "reply", "supply", "apply", "imply", "early", "ugly",
		"holy", "italy", "rely", "july", "silly", "lonely", "likely", "lovely":
		return false
	}
	return true
}

// findAllUTF16 locates every occurrence of needle (already lowercased) in
// haystackLower and returns UTF-16 offset spans into the ORIGINAL text.
//
// The conversion is the whole point: strings.Index gives byte offsets, and the
// client indexes UTF-16 code units.
func findAllUTF16(haystackLower, original, needle string) [][2]int {
	var out [][2]int
	from := 0
	for {
		idx := strings.Index(haystackLower[from:], needle)
		if idx < 0 {
			return out
		}
		byteStart := from + idx
		byteEnd := byteStart + len(needle)
		out = append(out, [2]int{
			utf16Len(original[:byteStart]),
			utf16Len(original[:byteEnd]),
		})
		from = byteEnd
	}
}

// utf16Len counts UTF-16 code units in s.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}
