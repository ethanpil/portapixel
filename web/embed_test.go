package web

import (
	"bytes"
	"os"
	"testing"
)

// The licence list is in two places: the root of the repository, which is what D33
// asks for, and this directory, which is what the binary embeds. A list that drifts
// is worse than no list, so the two must be the same bytes.
func TestLicensesMatchTheRepositoryCopy(t *testing.T) {
	embedded, err := Licenses()
	if err != nil {
		t.Fatalf("the binary embeds no %s: %v", LicensesName, err)
	}
	root, err := os.ReadFile("../" + LicensesName)
	if err != nil {
		t.Fatalf("read the copy at the root of the repository: %v", err)
	}
	if !bytes.Equal(embedded, root) {
		t.Errorf("web/%s and %s at the root of the repository are different. Copy one over the other.",
			LicensesName, LicensesName)
	}
}
