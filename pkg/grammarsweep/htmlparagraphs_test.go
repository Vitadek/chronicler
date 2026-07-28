package grammarsweep

import "testing"

func TestExtractParagraphs_SplitsOnPBoundaries(t *testing.T) {
	html := "<p>She quickly walked to the door.</p><p>She went to the store.</p>"
	got := extractParagraphs(html)
	if len(got) != 2 {
		t.Fatalf("expected 2 paragraphs, got %d: %+v", len(got), got)
	}
	if got[0] != "She quickly walked to the door." {
		t.Errorf("paragraph 0 = %q", got[0])
	}
	if got[1] != "She went to the store." {
		t.Errorf("paragraph 1 = %q", got[1])
	}
}

func TestExtractParagraphs_FlattensInlineMarks(t *testing.T) {
	html := "<p>She <strong>quickly</strong> walked to the <em>door</em>.</p>"
	got := extractParagraphs(html)
	if len(got) != 1 || got[0] != "She quickly walked to the door." {
		t.Fatalf("unexpected extraction: %+v", got)
	}
}

func TestExtractParagraphs_SkipsHeadingsAndShortParagraphs(t *testing.T) {
	// Grammar.ts's compute() only ever walks ProseMirror `paragraph` nodes and
	// filters anything under minChars=12 (trimmed) — this must match, or the
	// sweep caches text the client will never actually request.
	html := "<h1>Chapter One</h1><p>Ok.</p><p>This paragraph is definitely long enough to check.</p>"
	got := extractParagraphs(html)
	if len(got) != 1 {
		t.Fatalf("expected only the long paragraph, got %d: %+v", len(got), got)
	}
	if got[0] != "This paragraph is definitely long enough to check." {
		t.Errorf("unexpected paragraph: %q", got[0])
	}
}

func TestExtractParagraphs_EmptyInput(t *testing.T) {
	if got := extractParagraphs(""); got != nil {
		t.Errorf("expected nil for empty input, got %+v", got)
	}
	if got := extractParagraphs("   "); got != nil {
		t.Errorf("expected nil for whitespace-only input, got %+v", got)
	}
}

func TestExtractParagraphs_PreservesUntrimmedText(t *testing.T) {
	// The cache is keyed on the exact text the client sends (buildPosMap never
	// trims) — a paragraph with incidental leading/trailing space inside the
	// <p> must round-trip untrimmed, or its cache entry can never be hit by a
	// real request.
	html := "<p>  She quickly walked to the door.  </p>"
	got := extractParagraphs(html)
	if len(got) != 1 || got[0] != "  She quickly walked to the door.  " {
		t.Fatalf("expected untrimmed text preserved, got %+v", got)
	}
}
