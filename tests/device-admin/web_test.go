package webtest

import (
	"testing"

	"github.com/ethanpil/portapixel/tests/internal/nodetest"
)

// TestWebHelpers runs the node tests of this directory, so that go test ./...
// runs them too. nodetest.Run has the rules: the node version, the CI rule and
// the test cache.
func TestWebHelpers(t *testing.T) { nodetest.Run(t) }
