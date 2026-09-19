// Package opslog keeps the event log of the device.
//
// Why this package exists: the operator must be able to see what the device did
// after a fault, and a fault often includes a power cut. The log therefore goes
// straight to the disk on ext4 (D35), not to RAM. It also has a fixed maximum
// size: it trims itself to the newest 1000 lines after it passes 1200 lines, so
// it can never fill the partition.
package opslog
