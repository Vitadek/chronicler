package grammar

import (
	"strings"
	"sync"
	"testing"
)

func TestSuggest_IndexedDistanceTwoPreservesUsefulCorrection(t *testing.T) {
	d := load(t)
	const typo = "qikly" // "quickly" with two missing letters.

	// Pin this to the indexed fallback rather than accidentally exercising the
	// much cheaper one-edit path after a dictionary update.
	if oneEdit := d.knownEdits(edits1(typo)); len(oneEdit) != 0 {
		t.Fatalf("test typo unexpectedly has one-edit dictionary matches: %v", oneEdit)
	}

	suggestions := d.Suggest(typo)
	if len(suggestions) == 0 || suggestions[0] != "quickly" {
		t.Fatalf("Suggest(%q) = %v, want quickly first", typo, suggestions)
	}
}

func TestEditDistanceRows_UsesBoundAndTranspositions(t *testing.T) {
	rows := newEditDistanceRows(32)
	for _, tc := range []struct {
		a, b string
		max  int
		want bool
	}{
		{"qikly", "quickly", 2, true},
		{"recieved", "received", 1, true},
		{"kitten", "sitting", 2, false},
		{"kitten", "sitting", 3, true},
		{"same", "same", 0, true},
	} {
		if got := rows.within(tc.a, tc.b, tc.max); got != tc.want {
			t.Errorf("within(%q, %q, %d) = %v, want %v", tc.a, tc.b, tc.max, got, tc.want)
		}
	}
}

func TestSuggest_BoundsPathologicalWord(t *testing.T) {
	d := load(t)
	word := strings.Repeat("z", maxSuggestionWordLength+1)
	if got := d.Suggest(word); got != nil {
		t.Fatalf("Suggest(%d-byte word) = %v, want nil", len(word), got)
	}
}

func TestDictionaryCheck_Concurrent(t *testing.T) {
	d := load(t)
	const (
		workers = 8
		rounds  = 8
		text    = "She quikly walked to the door."
	)

	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				hits := d.Check(text)
				if len(hits) == 0 || !hasReplacement(hits[0], "quickly") {
					errs <- "concurrent check lost the quickly correction"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func BenchmarkSuggest_IndexedDistanceTwo(b *testing.B) {
	d, err := Load()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Suggest("qikly")
	}
}

func BenchmarkDictionaryCheck_Parallel(b *testing.B) {
	d, err := Load()
	if err != nil {
		b.Fatal(err)
	}
	const text = "She quikly walked to the door while they waited by the gate."
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			d.Check(text)
		}
	})
}
