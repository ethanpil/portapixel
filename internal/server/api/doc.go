// Package api holds the device-facing routes of the fleet server, /api/v1
// (plan section 12, contract section 5).
//
// Why this package exists: the routes that a device calls are a contract with
// every card in the field, and an old device must keep working. So they live
// apart from the admin routes, which change with the UI. This package holds
// routes only. Each rule about the data lives in internal/server/db, the media
// store, or the release mirror.
//
// Everything here is a pull: the device asks, the server answers, and the server
// never dials a device (D24).
package api
