package plugins

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmptyResolutionCollectionsEncodeAsArrays(t *testing.T) {
	resolution := Resolve([]ResolveInput{{PluginDeps: PluginDeps{ID: "chronicle.proofreader"}}}, []string{"host:grammar"})
	payload, err := json.Marshal(resolution)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	for _, field := range []string{"missing", "unmetWants", "conflictsWith", "shadowedCore", "activationOrder"} {
		if strings.Contains(encoded, `"`+field+`":null`) {
			t.Fatalf("%s must encode as []: %s", field, encoded)
		}
	}
}
