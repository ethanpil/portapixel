package scheduler

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/opslog"
)

// fake holds a clock and a configuration that a test can move.
type fake struct {
	now    time.Time
	synced bool
	cfg    config.Config
	s      *Scheduler
}

func newFake(t *testing.T, cfg config.Config) *fake {
	t.Helper()
	f := &fake{now: time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC), synced: true, cfg: cfg}
	f.s = New(Options{
		Config: func() config.Config { return f.cfg },
		Now:    func() time.Time { return f.now },
		Synced: func() bool { return f.synced },
		Log:    opslog.New(filepath.Join(t.TempDir(), "ops.log")),
	})
	return f
}

// at moves the clock to a weekday and a time. The reference week is
// 2026-09-14 (Monday) to 2026-09-20 (Sunday).
func (f *fake) at(day time.Weekday, hour, minute int) {
	monday := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	offset := (int(day) + 6) % 7 // Monday is 0
	f.now = monday.AddDate(0, 0, offset).Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
}

func weekdayRules() config.Config {
	cfg := config.Default()
	cfg.Playback.DefaultPlaylist = "default"
	cfg.Schedule = []config.Rule{
		{Playlist: "morning", Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "08:00", End: "12:00"},
		{Playlist: "afternoon", Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "12:00", End: "18:00"},
		{Playlist: "weekend", Days: []string{"sat", "sun"}, Start: "00:00", End: "23:59"},
		{Playlist: "night", Start: "22:00", End: "02:00"}, // past midnight, every day
	}
	return cfg
}

func TestEvaluateRules(t *testing.T) {
	tests := []struct {
		name string
		day  time.Weekday
		h, m int
		want string
	}{
		{"weekday morning", time.Monday, 9, 0, "morning"},
		{"weekday at the start", time.Monday, 8, 0, "morning"},
		{"weekday at the end is the next rule", time.Monday, 12, 0, "afternoon"},
		{"weekday after the rules", time.Monday, 19, 0, "default"},
		{"weekday late is the night rule", time.Monday, 22, 30, "night"},
		{"after midnight is still the night rule", time.Tuesday, 1, 0, "night"},
		{"after the night rule", time.Tuesday, 2, 0, "default"},
		{"before the morning rule", time.Tuesday, 7, 59, "default"},
		{"saturday", time.Saturday, 9, 0, "weekend"},
		{"sunday evening", time.Sunday, 19, 0, "weekend"},
		{"saturday late is the night rule", time.Saturday, 23, 0, "weekend"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t, weekdayRules())
			f.at(tt.day, tt.h, tt.m)
			if got := f.s.Evaluate(); got != tt.want {
				t.Errorf("at %s %02d:%02d got %q, want %q", tt.day, tt.h, tt.m, got, tt.want)
			}
		})
	}
}

func TestFirstMatchWins(t *testing.T) {
	cfg := config.Default()
	cfg.Schedule = []config.Rule{
		{Playlist: "first", Start: "08:00", End: "18:00"},
		{Playlist: "second", Start: "09:00", End: "10:00"},
	}
	f := newFake(t, cfg)
	f.at(time.Monday, 9, 30)
	if got := f.s.Evaluate(); got != "first" {
		t.Fatalf("got %q, want first", got)
	}
}

func TestNoClockHoldsTheDefault(t *testing.T) {
	f := newFake(t, weekdayRules())
	f.synced = false
	f.at(time.Monday, 9, 0)
	if got := f.s.Evaluate(); got != "default" {
		t.Fatalf("got %q, want default while the clock is not synchronised", got)
	}
	f.synced = true
	if got := f.s.Evaluate(); got != "morning" {
		t.Fatalf("got %q after the clock synchronised, want morning", got)
	}
}

func TestTimezone(t *testing.T) {
	cfg := config.Default()
	cfg.Device.Timezone = "America/New_York"
	cfg.Schedule = []config.Rule{{Playlist: "office", Start: "08:00", End: "18:00"}}
	f := newFake(t, cfg)

	// 12:00 UTC is 08:00 in New York in September.
	f.now = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	if got := f.s.Evaluate(); got != "office" {
		t.Fatalf("got %q, want office", got)
	}
	// 11:00 UTC is 07:00 in New York: before the rule.
	f.now = time.Date(2026, 9, 14, 11, 0, 0, 0, time.UTC)
	if got := f.s.Evaluate(); got != cfg.Playback.DefaultPlaylist {
		t.Fatalf("got %q, want the default playlist", got)
	}
}

func TestUnknownTimezoneFallsBackToUTC(t *testing.T) {
	cfg := config.Default()
	cfg.Device.Timezone = "Mars/Olympus"
	cfg.Schedule = []config.Rule{{Playlist: "office", Start: "08:00", End: "18:00"}}
	f := newFake(t, cfg)
	f.now = time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	if got := f.s.Evaluate(); got != "office" {
		t.Fatalf("got %q, want office in UTC", got)
	}
}

func TestBadRuleTimesMatchNothing(t *testing.T) {
	cfg := config.Default()
	cfg.Schedule = []config.Rule{
		{Playlist: "broken", Start: "8am", End: "noon"},
		{Playlist: "empty", Start: "10:00", End: "10:00"},
		{Playlist: "missing"},
	}
	f := newFake(t, cfg)
	f.at(time.Monday, 10, 0)
	if got := f.s.Evaluate(); got != cfg.Playback.DefaultPlaylist {
		t.Fatalf("got %q, want the default playlist", got)
	}
}

func TestSubscribersGetTheChange(t *testing.T) {
	f := newFake(t, weekdayRules())
	ch, cancel := f.s.Subscribe()
	defer cancel()

	f.at(time.Monday, 9, 0)
	f.s.Evaluate()
	select {
	case name := <-ch:
		if name != "morning" {
			t.Fatalf("event = %q", name)
		}
	default:
		t.Fatal("no event after the first evaluation")
	}

	// The same answer again must send nothing.
	f.s.Evaluate()
	select {
	case name := <-ch:
		t.Fatalf("an event %q without a change", name)
	default:
	}

	// A change with nobody reading must keep the newest name only.
	f.at(time.Monday, 13, 0)
	f.s.Evaluate()
	f.at(time.Monday, 19, 0)
	f.s.Evaluate()
	if name := <-ch; name != "default" {
		t.Fatalf("event = %q, want the newest name", name)
	}
	if f.s.Active() != "default" {
		t.Fatalf("Active = %q", f.s.Active())
	}
}

func TestFleetRulesReplaceTheTOML(t *testing.T) {
	f := newFake(t, weekdayRules())
	f.at(time.Monday, 9, 0)
	if got := f.s.Evaluate(); got != "morning" {
		t.Fatalf("got %q", got)
	}

	f.s.SetFleetRules("fleet-default", []manifest.Rule{
		{Playlist: "fleet-day", Days: []string{"mon"}, Start: "08:00", End: "17:00"},
	}, nil)
	if got := f.s.Active(); got != "fleet-day" {
		t.Fatalf("got %q, want fleet-day", got)
	}
	f.at(time.Monday, 18, 0)
	if got := f.s.Evaluate(); got != "fleet-default" {
		t.Fatalf("got %q, want fleet-default", got)
	}
	f.s.ClearFleetRules()
	if got := f.s.Active(); got != "default" {
		t.Fatalf("got %q after the unpair, want default", got)
	}
}

func screenConfig(on, off string, days []string) config.Config {
	cfg := config.Default()
	cfg.Display.OnTime = on
	cfg.Display.OffTime = off
	cfg.Display.PowerDays = days
	return cfg
}

func TestScreenSchedule(t *testing.T) {
	tests := []struct {
		name  string
		cfg   config.Config
		day   time.Weekday
		h, m  int
		wants bool
	}{
		{"no schedule is always on", config.Default(), time.Sunday, 3, 0, true},
		{"inside the window", screenConfig("07:30", "22:00", nil), time.Monday, 12, 0, true},
		{"at the on time", screenConfig("07:30", "22:00", nil), time.Monday, 7, 30, true},
		{"at the off time", screenConfig("07:30", "22:00", nil), time.Monday, 22, 0, false},
		{"before the on time", screenConfig("07:30", "22:00", nil), time.Monday, 3, 30, false},
		{"past midnight, late", screenConfig("18:00", "02:00", nil), time.Monday, 23, 0, true},
		{"past midnight, early", screenConfig("18:00", "02:00", nil), time.Tuesday, 1, 0, true},
		{"past midnight, gap", screenConfig("18:00", "02:00", nil), time.Tuesday, 3, 0, false},
		{"a day that is not in the list", screenConfig("07:30", "22:00", []string{"mon", "tue", "wed", "thu", "fri"}), time.Saturday, 12, 0, false},
		{"a day that is in the list", screenConfig("07:30", "22:00", []string{"sat"}), time.Saturday, 12, 0, true},
		{"past midnight, the start day counts", screenConfig("18:00", "02:00", []string{"fri"}), time.Saturday, 1, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t, tt.cfg)
			f.at(tt.day, tt.h, tt.m)
			if got := f.s.ScreenShouldBeOn(f.now); got != tt.wants {
				t.Errorf("ScreenShouldBeOn = %v, want %v", got, tt.wants)
			}
		})
	}
}

func TestScreenOffCovers(t *testing.T) {
	// No schedule: nothing is covered, so the nightly restart runs.
	f := newFake(t, config.Default())
	f.at(time.Monday, 3, 30)
	if f.s.ScreenOffCovers(f.now) {
		t.Errorf("a device with no screen schedule reports cover")
	}
	if f.s.HasScreenSchedule() {
		t.Errorf("HasScreenSchedule is true with no times")
	}

	// A night that the screen is off covers the 03:30 restart.
	f = newFake(t, screenConfig("07:30", "22:00", nil))
	f.at(time.Monday, 3, 30)
	if !f.s.ScreenOffCovers(f.now) {
		t.Errorf("the screen is off at 03:30 but the cover is false")
	}
	f.at(time.Monday, 12, 0)
	if f.s.ScreenOffCovers(f.now) {
		t.Errorf("the screen is on at noon but the cover is true")
	}
}

func TestFleetScreenRuleWins(t *testing.T) {
	f := newFake(t, screenConfig("07:30", "22:00", nil))
	f.s.SetFleetRules("default", nil, &manifest.ScreenRule{OnTime: "10:00", OffTime: "11:00"})
	f.at(time.Monday, 8, 0)
	if f.s.ScreenShouldBeOn(f.now) {
		t.Errorf("the device used the TOML window and not the fleet window")
	}
	f.at(time.Monday, 10, 30)
	if !f.s.ScreenShouldBeOn(f.now) {
		t.Errorf("the fleet window did not turn the screen on")
	}
}

func TestClockSyncedFallsBackToTheYear(t *testing.T) {
	// On Linux the kernel answers and the year does not come into it. Away from
	// Linux the year is the whole test, and a clock in 1970 is never true.
	if _, ok := kernelSynced(); ok {
		t.Skip("the kernel of this machine answers the clock question itself")
	}
	if ClockSynced(func() time.Time { return time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC) }) {
		t.Error("a clock that says 1970 was called synchronised")
	}
	if !ClockSynced(func() time.Time { return time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC) }) {
		t.Error("a clock that says 2026 was not called synchronised")
	}
}

// The scheduler had a clock parser of its own, and it took values that the
// configuration validator refused: "8:30" went in a schedule rule but never in a
// rendered file, and the nightly restart read the same value with a third parser
// and said no. config.ParseClock is the one format now.
func TestScheduleTimesUseTheOneClockFormat(t *testing.T) {
	now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC) // a Friday
	tests := []struct {
		name  string
		start string
		end   string
		want  bool
	}{
		{"a rendered time matches", "08:30", "18:00", true},
		{"a time outside the window does not", "10:00", "18:00", false},
		{"a short hour is not a time", "8:30", "18:00", false},
		{"a short minute is not a time", "08:30", "18:0", false},
		{"a time with spaces is not a time", " 08:30", "18:00", false},
		{"seconds are not a time", "08:30:00", "18:00", false},
		{"an empty value is not a time", "", "18:00", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inWindow(tt.start, tt.end, now, nil); got != tt.want {
				t.Errorf("inWindow(%q, %q) = %v, want %v", tt.start, tt.end, got, tt.want)
			}
		})
	}
}
