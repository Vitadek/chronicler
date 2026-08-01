package languagetool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestCheckSendsPickyLevelOnlyWhenRequested(t *testing.T) {
	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		_, _ = w.Write([]byte(`{"matches":[]}`))
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, lang: "en-US", http: server.Client()}
	if _, err := client.Check(context.Background(), "A sentence.", "picky"); err != nil {
		t.Fatal(err)
	}
	if got.Get("level") != "picky" {
		t.Fatalf("level = %q, want picky", got.Get("level"))
	}

	if _, err := client.Check(context.Background(), "Another sentence.", "standard"); err != nil {
		t.Fatal(err)
	}
	if got.Get("level") != "" {
		t.Fatalf("standard request sent level = %q", got.Get("level"))
	}
}
