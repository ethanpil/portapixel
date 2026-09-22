package db

import (
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
)

// The setting keys. The settings table holds what the admin UI can change while
// the server runs. server.toml holds what needs a restart: the listen address,
// the certificate and the trusted proxies.
const (
	// SettingServerName is the name that the devices see in the manifest.
	SettingServerName = "server_name"
	// SettingPollSeconds is the poll interval of a device that has no value of
	// its own.
	SettingPollSeconds = "default_poll_seconds"
)

// MinPollSeconds and MaxPollSeconds are the limits of the poll interval.
//
// The floor is 10 and not 5. The device validator repairs a smaller value to its
// own default, so a server that offered 5 would hand out a number that the screen
// silently ignores. One floor holds for the two ends.
const (
	MinPollSeconds     = 10
	MaxPollSeconds     = 86400
	DefaultPollSeconds = 60
)

// Setting gives one value, or the fallback when the table holds none.
//
// A fault of the database is not "the table holds none". The caller of this
// function keeps the values of the manifest in memory, so a read that failed and
// answered the fallback would give the whole fleet the default server name and the
// default poll interval until the next admin write. The log line says which one it
// was.
func (d *DB) Setting(key, fallback string) string {
	var v string
	err := d.r.QueryRow(`SELECT v FROM settings WHERE k = ?`, key).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fallback
	case err != nil:
		slog.Error("read a setting", "key", key, "error", err)
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
