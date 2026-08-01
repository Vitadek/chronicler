package api

import (
	"testing"

	"chronicle-server/pkg/grammar"
)

func TestGroupOverlappingHitsKeepsProviderCommentaryTogether(t *testing.T) {
	hits := []grammar.Hit{
		{Start: 8, End: 12, SourceID: "harper"},
		{Start: 8, End: 19, SourceID: "proselint"},
		{Start: 30, End: 34, SourceID: "native"},
	}
	groupOverlappingHits(hits)
	if hits[0].GroupID == "" || hits[0].GroupID != hits[1].GroupID {
		t.Fatalf("overlapping hits were not grouped: %#v", hits)
	}
	if hits[2].GroupID == hits[0].GroupID {
		t.Fatalf("separate hit joined overlap group: %#v", hits)
	}
}
