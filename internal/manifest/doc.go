// Package manifest holds the wire types of the fleet protocol.
//
// Why this package exists: the device and the server are two programs in one Go
// module. If each one had its own copy of the JSON types, the two ends of the
// protocol would drift apart. Both ends import these types instead, so the
// compiler finds a change that only one end made.
//
// The Status type is here for the same reason. The device serves it at
// /api/status and also sends it in each fleet heartbeat.
package manifest
