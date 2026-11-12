//go:build !linux

package priority

// Host gives no System away from Linux. Apply then does nothing, so a
// development machine keeps its own priorities.
func Host() System { return nil }
