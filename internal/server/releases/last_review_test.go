package releases

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestATagIsKeptInTheFormOfTheDevice covers the version name of a GitHub release.
//
// The server kept the tag "v1.5.0" and a device reports "1.5.0". The manifest gate
// compares the two, so each manifest named the release for a screen that already ran
// it, and the Versions page never matched the fleet to the approved version.
func TestATagIsKeptInTheFormOfTheDevice(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"v1.5.0","body":"notes","assets":[`+
			`{"name":"SHA256SUMS","browser_download_url":"https://github.com/o/n/SHA256SUMS"}]}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	l := NewLister("owner/name")
	l.BaseURL = srv.URL

	rel, err := l.Release(context.Background(), "1.5.0")
	if err != nil {
		t.Fatalf("the release 1.5.0 of the tag v1.5.0 is not found: %v", err)
	}
	if rel.Version != "1.5.0" || rel.Assets["SHA256SUMS"] == "" {
		t.Fatalf("the release is %+v", rel)
	}
}
