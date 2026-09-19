package db

import (
	"database/sql"
	"errors"
	"strconv"
)

// The setting keys. The settings table holds what the admin UI can change while
// the server runs. server.toml holds what needs a restart: the listen address,
// the certificate and the public URL.
const (
	// SettingServerName is the name that the devices see in the manifest.
	SettingServerName = "server_name"
	// SettingPollSeconds is the poll interval of a device that has no value of
	// its own.
	SettingPollSeconds = "default_poll_seconds"
)

// Setting gives one value, or the fallback when the table holds none.
func (d *DB) Setting(key, fallback string) string {
	var v string
	err := d.r.QueryRow(`SELECT v FROM settings WHERE k = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return fallback
	}
	return v
}

// SettingInt gives one value as a number.
func (d *DB) SettingInt(key string, fallback int) int {
	v, err := strconv.Atoi(d.Setting(key, ""))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

// SetSetting writes one value.
func (d *DB) SetSetting(key, value string) error {
	_, err := d.w.Exec(`INSERT INTO settings (k, v) VALUES (?, ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v`, key, value)
	return err
}
