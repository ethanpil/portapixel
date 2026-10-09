package releases

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTheListerKeepsThePrereleaseFlag covers the mark on the Versions page. The
// lister dropped the flag, so an admin could approve a release candidate as if it
// were final.
func TestTheListerKeepsThePrereleaseFlag(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[`+
			`{"tag_name":"v0.5.0-rc.2","body":"candidate","prerelease":true,"assets":[]},`+
			`{"tag_name":"v0.4.0","body":"final","prerelease":false,"assets":[]}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	l := NewLister("owner/name")
	l.BaseURL = srv.URL

	list, errText, _ := l.List(context.Background(), true)
	if errText != "" {
		t.Fatalf("the list failed: %s", errText)
	}
	flags := map[string]bool{}
	for _, rel := range list {
		flags[rel.Version] = rel.Prerelease
	}
	if len(flags) != 2 || !flags["0.5.0-rc.2"] || flags["0.4.0"] {
		t.Fatalf("the pre-release flags are %v, want 0.5.0-rc.2 true and 0.4.0 false", flags)
	}
}
