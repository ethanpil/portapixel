package config

import "slices"

// Mask is the value that the API sends in place of a secret. The web UI shows
// it, and a save that sends it back keeps the secret that the device already
// has. An empty value from the UI clears the secret, so the user can still
// remove a WiFi key.
const Mask = "********"

// Masked gives a copy of the configuration with the secrets replaced. The API
// serves this copy: the LAN must never see the WiFi key, the admin password or
// the fleet token. A secret that is empty stays empty, so the UI can see that
// there is no value.
func (c Config) Masked() Config {
	out := c.clone()
	if out.Network.WifiPSK != "" {
		out.Network.WifiPSK = Mask
	}
	if out.Web.Password != "" {
		out.Web.Password = Mask
	}
	if out.Server.Token != "" {
		out.Server.Token = Mask
	}
	return out
}

// MergeMasked takes the new configuration from the API and puts the old secrets
// back into each field that holds the mask. PUT /api/config gets the masked copy
// from the UI, so without this step a save would delete every secret.
func MergeMasked(old, incoming Config) Config {
	out := incoming.clone()
	if out.Network.WifiPSK == Mask {
		out.Network.WifiPSK = old.Network.WifiPSK
	}
	if out.Web.Password == Mask {
		out.Web.Password = old.Web.Password
	}
	if out.Server.Token == Mask {
		out.Server.Token = old.Server.Token
	}
	return out
}

// clone gives a copy of the configuration that shares no slice with it. A plain
// copy of a Config keeps the slices of the original, so a change to the DNS
// list, the power days or a schedule rule in one copy would reach the other.
// Masked and MergeMasked both hand a copy to another part of the daemon, so the
// copy must stand alone.
func (c Config) clone() Config {
	out := c
	out.Network.DNS = slices.Clone(c.Network.DNS)
	out.Display.PowerDays = slices.Clone(c.Display.PowerDays)
	out.Schedule = slices.Clone(c.Schedule)
	for i := range out.Schedule {
		out.Schedule[i].Days = slices.Clone(c.Schedule[i].Days)
	}
	return out
}
