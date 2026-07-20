package collab

import (
	"encoding/hex"
	"net/http/httptest"
	"testing"

	"github.com/reearth/ygo/crdt"
)

func TestRoomNameFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/collab", want: ""},
		{path: "/collab/", want: ""},
		{path: "/collab/user-id/manuscript:chapter", want: "user-id/manuscript:chapter"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("GET", tt.path, nil)
		if got := roomNameFromPath(r); got != tt.want {
			t.Errorf("roomNameFromPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

// Generated with yjs 13.6.27, the exact version used by Chronicler's frontend
// and formal collaboration client. This protects the Go CRDT boundary against
// silently accepting a frame without integrating its shared types.
func TestYjsMapUpdateInteroperability(t *testing.T) {
	update, err := hex.DecodeString("0101ddad8eb00600280106666f726d616c106475726162696c6974792d70726f6f66017714636f6e7665726765642de69db1e4baac2de29c9300")
	if err != nil {
		t.Fatal(err)
	}
	doc := crdt.New()
	if err := crdt.ApplyUpdateV1(doc, update, nil); err != nil {
		t.Fatalf("apply Yjs update: %v", err)
	}
	value, ok := doc.GetMap("formal").Get("durability-proof")
	if !ok || value != "converged-東京-✓" {
		t.Fatalf("unexpected shared map value: %#v, present=%v", value, ok)
	}
}
