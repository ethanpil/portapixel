// Package fsutil writes files in a way that survives a power cut.
//
// Why this package exists: PPMEDIA is exFAT and has no journal. A write that
// stops in the middle must never destroy the old file. Every deliberate write
// therefore goes to a temporary file in the same directory, gets an fsync, and
// then replaces the old file with a rename (D41). One package holds that rule,
// so no caller can forget a step.
package fsutil
