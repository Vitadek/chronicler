package api

import "testing"

func statePointer(value string) *string { return &value }

func TestPluginStateBlobsSeparatesGlobalAndManuscriptScopes(t *testing.T) {
	rows := []UserPluginState{
		{PluginID: "chronicle.proofreader", State: `{"checks":{"spelling":false}}`},
		{PluginID: "chronicle.proofreader", ManuscriptID: statePointer("book-a"), State: `{"dismissed":["a"]}`},
		{PluginID: "chronicle.proofreader", ManuscriptID: statePointer("book-b"), State: `{"dismissed":["b"]}`},
		{PluginID: "another.plugin", ManuscriptID: statePointer("book-a"), State: `{"other":true}`},
	}

	global, manuscripts := pluginStateBlobs(rows, "chronicle.proofreader")
	if global != `{"checks":{"spelling":false}}` {
		t.Fatalf("global state = %q", global)
	}
	if len(manuscripts) != 2 || manuscripts["book-a"] != `{"dismissed":["a"]}` || manuscripts["book-b"] != `{"dismissed":["b"]}` {
		t.Fatalf("manuscript states = %#v", manuscripts)
	}
}

func TestPluginStateBlobsDefaultsWithoutRecords(t *testing.T) {
	global, manuscripts := pluginStateBlobs(nil, "chronicle.proofreader")
	if global != "{}" || len(manuscripts) != 0 {
		t.Fatalf("global=%q manuscripts=%#v", global, manuscripts)
	}
}
