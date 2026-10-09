package server_test

import (
	"net/http"
	"testing"
)

// TestTheReleaseListMarksAPrerelease covers the Versions page. A GitHub release
// that is a pre-release showed no mark, so an admin could approve a release
// candidate as if it were final.
func TestTheReleaseListMarksAPrerelease(t *testing.T) {
	for _, c := range []struct {
		version string
		pre     bool
	}{
		{"0.5.0-rc.1", true},
		{"0.5.0", false},
	} {
		t.Run(c.version, func(t *testing.T) {
			// One fleet for each case: the lister keeps its answer for ten minutes.
			f := newFleet(t)
			f.login()
			rel := signRelease(t, c.version)
			rel.prerelease = c.pre
			f.useRelease(rel)

			res := f.mustOK(f.adminCall(http.MethodGet, "/api/admin/releases", nil), "the release list")
			var view struct {
				Releases []struct {
					Version    string `json:"version"`
					Prerelease bool   `json:"prerelease"`
				} `json:"releases"`
			}
			res.json(t, &view)
			if len(view.Releases) != 1 || view.Releases[0].Version != c.version {
				t.Fatalf("the list is %+v", view.Releases)
			}
			if view.Releases[0].Prerelease != c.pre {
				t.Errorf("%s has prerelease %v in the list, want %v", c.version, view.Releases[0].Prerelease, c.pre)
			}
		})
	}
}
