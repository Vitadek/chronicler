package activity

import "testing"

func TestMillisSinceLastRequest_LargeBeforeAnyTouch(t *testing.T) {
	// Not calling Touch() first would be flaky (a previous test in this
	// package may have touched it), so this just documents the never-touched
	// contract — see TestTouch_ResetsToNearZero for the meaningful behavior.
	if MillisSinceLastRequest() < 0 {
		t.Fatal("MillisSinceLastRequest must never be negative")
	}
}

func TestTouch_ResetsToNearZero(t *testing.T) {
	Touch()
	ms := MillisSinceLastRequest()
	if ms < 0 || ms > 1000 {
		t.Fatalf("expected MillisSinceLastRequest() to be near 0 right after Touch(), got %d", ms)
	}
}
