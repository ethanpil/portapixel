// Package sigverify checks minisign signatures.
//
// Why this package exists: the updater must never run a binary that the project
// did not sign (D47). The device and the server both need that check, and both
// must do it the same way. The check is in one place, so it cannot be half
// applied. The mirror on the fleet server is a cache, not a trust anchor: the
// device verifies the signature again before it swaps the release.
package sigverify
