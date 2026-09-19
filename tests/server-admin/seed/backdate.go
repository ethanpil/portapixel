package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // the same pure-Go driver that the server uses
)

// backdate moves the last contact of two screens into the past.
//
// No admin route can do this, and it is the only way to see the quiet state and
// the offline state without a wait of a day. The server must be stopped: two
// writers on one SQLite file is a fight that this tool does not need to win.
func backdate(dataDir string) error {
	if dataDir == "" {
		return fmt.Errorf("backdate needs -data, the data directory of the server")
	}
	path := filepath.Join(dataDir, "portapixel.db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()

	now := time.Now().UTC()
	moves := []struct {
		id   string
		when time.Time
		what string
	}{
		// Quiet: it called today, but longer ago than 2.5 poll intervals.
		{"px-7c33b190", now.Add(-18 * time.Minute), "quiet"},
		// Offline: it has not called for more than a day.
		{"px-5e10d8b7", now.Add(-74 * time.Hour), "offline"},
		// Older than the clone window, so the next new hardware ID reads as a
		// repair and not as a clone.
		{"px-9a02f451", now.Add(-40 * time.Minute), "ready for a hardware swap"},
	}
	for _, m := range moves {
		res, err := db.Exec(`UPDATE devices SET last_seen = ? WHERE id = ?`,
			m.when.Format(time.RFC3339), m.id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("%s is not in the database; run fill first", m.id)
		}
		fmt.Printf("%s is now %s\n", m.id, m.what)
	}
	return fakeReleases(db, now)
}

// fakeReleases writes release rows. A development machine cannot reach the
// public release page, and the mirror states are what the Versions page draws,
// so the rows come from here.
func fakeReleases(db *sql.DB, now time.Time) error {
	rows := []struct {
		version, notes, state, failure string
		days                           int
		approved                       bool
	}{
		{"1.5.1", "Faster startup, and a stall is fixed when a web page item times out. Two days old, so try it on one screen first.", "idle", "", 2, false},
		{"1.5.0", "Web page items reload on an interval. The overnight screen-off skips the nightly restart.", "done", "", 45, true},
		{"1.4.2", "Fixes HDMI sound picking the wrong output on some x86 boxes.", "failed", "the release page did not answer", 69, false},
		{"1.4.1", "Withdrawn: it rolled back on three screens during testing.", "idle", "", 78, false},
	}
	for _, r := range rows {
		approved := 0
		approvedAt := ""
		if r.approved {
			approved = 1
			approvedAt = now.Format(time.RFC3339)
		}
		_, err := db.Exec(`INSERT INTO releases (version, approved, notes, published_at, approved_at, mirror_state, mirror_error)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(version) DO UPDATE SET notes = excluded.notes,
				mirror_state = excluded.mirror_state, mirror_error = excluded.mirror_error`,
			r.version, approved, r.notes,
			now.AddDate(0, 0, -r.days).Format(time.RFC3339), approvedAt, r.state, r.failure)
		if err != nil {
			return err
		}
	}
	fmt.Printf("%d releases\n", len(rows))
	return nil
}
