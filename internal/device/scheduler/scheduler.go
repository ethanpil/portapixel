package scheduler

import (
	"slices"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/opslog"
)

// tick is the time between two evaluations. The rules have a resolution of one
// minute, so 20 seconds is enough to cross every boundary in time and cheap
// enough to ignore.
const tick = 20 * time.Second

// dayNames maps a weekday to the name that the TOML uses.
var dayNames = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// Options are the parameters of a Scheduler.
type Options struct {
	// Config gives the configuration that the daemon holds now. The scheduler
	// reads it at each evaluation, so a hand edit of the TOML takes effect
	// without a restart.
	Config func() config.Config
	// Now gives the time. A test gives a fixed time.
	Now func() time.Time
	// Synced reports if the clock is true (D40).
	Synced func() bool
	Log    *opslog.Log
}

// Scheduler evaluates the two schedules. It is safe for use by more than one
// goroutine.
type Scheduler struct {
	opt Options

	mu     sync.Mutex
	active string
	// gated is true while the clock is not synchronised. It stops the ops log
	// from writing the same line every 20 seconds.
	gated bool
	// The rules of the fleet server, when the device is paired.
	fleet        bool
	fleetDefault string
	fleetRules   []manifest.Rule
	fleetScreen  *manifest.ScreenRule

	subs   map[int]chan string
	nextID int
}

// locations caches the time zone objects. time.LoadLocation reads files, and the
// daemon asks for the local time once a second for months.
var locations sync.Map // string -> *time.Location

// Location gives the time zone of a name. An unknown name gives UTC: the
// configuration check already reports a bad name, and a schedule in UTC is better
// than no schedule.
//
// It is here and it is exported. The daemon needs the same cache for the local
// time that it gives to the scheduler and to the browser supervisor.
func Location(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	if loc, ok := locations.Load(name); ok {
		return loc.(*time.Location)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = time.UTC
	}
	locations.Store(name, loc)
	return loc
}

// New makes a Scheduler. It does not evaluate: the caller calls Evaluate after
// it wires the subscribers, so the first change event is not lost.
func New(opt Options) *Scheduler {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Synced == nil {
		opt.Synced = func() bool { return true }
	}
	return &Scheduler{
		opt:  opt,
		subs: make(map[int]chan string),
	}
}

// Active gives the playlist name that must play now.
func (s *Scheduler) Active() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Subscribe gives a channel that carries the name of the new playlist at each
// change. The channel holds one name: a subscriber that is busy gets the newest
// name, never a queue of old ones. The second answer removes the subscriber.
func (s *Scheduler) Subscribe() (<-chan string, func()) {
	ch := make(chan string, 1)

	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.subs[id] = ch
	s.mu.Unlock()

	return ch, func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

// Run evaluates the schedule until done is closed.
func (s *Scheduler) Run(done <-chan struct{}) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			s.Evaluate()
		}
	}
}

// Evaluate finds the playlist for this minute and gives its name. It tells the
// subscribers when the name is different from the name before.
//
// The new name and the message go out under the same lock. The ticker, the fleet
// client and an HTTP handler all call this. A send outside the lock let an older
// evaluation land last. The device then played the playlist that /api/status did
// not name, until the next change.
//
// The ops log lines go out after the lock. A write to the log goes to a file, and
// Active() is on the loop of the browser supervisor.
func (s *Scheduler) Evaluate() string {
	cfg := s.config()
	now := s.opt.Now().In(Location(cfg.Device.Timezone))
	synced := s.opt.Synced()

	name := s.defaultPlaylist(cfg)
	if synced {
		if rule := s.match(cfg, now); rule != "" {
			name = rule
		}
	}

	var lines [][2]string
	s.mu.Lock()
	// The clock gate: say it once when it starts and once when it ends.
	switch {
	case !synced && !s.gated:
		s.gated = true
		lines = append(lines, [2]string{"scheduler.clock.wait", "the clock is not synchronised yet; " + name + " plays"})
	case synced && s.gated:
		s.gated = false
		lines = append(lines, [2]string{"scheduler.clock.ok", "the clock is synchronised; the schedule rules are live"})
	}
	changed := name != s.active
	s.active = name
	if changed {
		lines = append(lines, [2]string{"scheduler.playlist", name})
		for _, ch := range s.subs {
			// Never block: a subscriber that is busy reads the newest name.
			select {
			case ch <- name:
			default:
				select {
				case <-ch:
				default:
				}
				select {
				case ch <- name:
				default:
				}
			}
		}
	}
	s.mu.Unlock()

	for _, l := range lines {
		s.log(l[0], l[1])
	}
	return name
}

// SetFleetRules takes the schedule of the fleet server. The device uses it in
// place of the rules in the TOML while it is paired (D48). A nil call gives the
// TOML back.
func (s *Scheduler) SetFleetRules(defaultPlaylist string, rules []manifest.Rule, screen *manifest.ScreenRule) {
	s.mu.Lock()
	s.fleet = true
	s.fleetDefault = defaultPlaylist
	s.fleetRules = rules
	s.fleetScreen = screen
	s.mu.Unlock()
	s.Evaluate()
}

// ClearFleetRules goes back to the rules in the TOML. The daemon calls it when
// the device is unpaired.
func (s *Scheduler) ClearFleetRules() {
	s.mu.Lock()
	s.fleet = false
	s.fleetDefault = ""
	s.fleetRules = nil
	s.fleetScreen = nil
	s.mu.Unlock()
	s.Evaluate()
}

// ScreenShouldBeOn reports if the screen schedule wants the screen on at t. With
// no schedule the answer is always true.
func (s *Scheduler) ScreenShouldBeOn(t time.Time) bool {
	on, off, days, ok := s.screenRule()
	if !ok {
		return true
	}
	cfg := s.config()
	return inWindow(on, off, t.In(Location(cfg.Device.Timezone)), days)
}

// ScreenOffCovers reports if the screen schedule has the screen off at t. The
// nightly browser restart uses it: a restart at 03:30 is pointless when the
// screen is off then and the browser is not running (plan 3.3).
func (s *Scheduler) ScreenOffCovers(t time.Time) bool {
	if _, _, _, ok := s.screenRule(); !ok {
		return false
	}
	return !s.ScreenShouldBeOn(t)
}

// HasScreenSchedule reports if a screen schedule is set.
func (s *Scheduler) HasScreenSchedule() bool {
	_, _, _, ok := s.screenRule()
	return ok
}

// screenRule gives the live screen schedule: the fleet rule when the device is
// paired, else the [display] keys.
func (s *Scheduler) screenRule() (on, off string, days []string, ok bool) {
	s.mu.Lock()
	fleet, rule := s.fleet, s.fleetScreen
	s.mu.Unlock()

	if fleet && rule != nil {
		if rule.OnTime == "" || rule.OffTime == "" {
			return "", "", nil, false
		}
		return rule.OnTime, rule.OffTime, rule.Days, true
	}
	cfg := s.config()
	if cfg.Display.OnTime == "" || cfg.Display.OffTime == "" {
		return "", "", nil, false
	}
	return cfg.Display.OnTime, cfg.Display.OffTime, cfg.Display.PowerDays, true
}

// match gives the playlist of the first rule that matches, or "".
func (s *Scheduler) match(cfg config.Config, now time.Time) string {
	s.mu.Lock()
	fleet, rules := s.fleet, s.fleetRules
	s.mu.Unlock()

	if fleet {
		for _, r := range rules {
			if inWindow(r.Start, r.End, now, r.Days) {
				return r.Playlist
			}
		}
		return ""
	}
	for _, r := range cfg.Schedule {
		if inWindow(r.Start, r.End, now, r.Days) {
			return r.Playlist
		}
	}
	return ""
}

// defaultPlaylist gives the playlist that plays when no rule matches.
func (s *Scheduler) defaultPlaylist(cfg config.Config) string {
	s.mu.Lock()
	fleet, name := s.fleet, s.fleetDefault
	s.mu.Unlock()

	if fleet && name != "" {
		return name
	}
	return cfg.Playback.DefaultPlaylist
}

func (s *Scheduler) config() config.Config {
	if s.opt.Config == nil {
		return config.Default()
	}
	return s.opt.Config()
}

// log writes an ops log line. The caller may hold the lock, so this must not
// call back into the scheduler.
func (s *Scheduler) log(event, details string) {
	if s.opt.Log != nil {
		s.opt.Log.Log(event, details)
	}
}

// inWindow reports if now is inside the window from start to end on a permitted
// day.
//
// Both times empty means the whole day. A rule of "weekends: this playlist" needs
// no hours, and a rule that named the days and no hours matched nothing at all
// before: config.ParseClock refused the empty value and the rule was silently
// dead. The two times are both-or-neither everywhere (config.Validate holds the
// same rule).
//
// An end before the start goes past midnight. The day list then names the day on
// which the window starts: a Friday night rule that ends at 02:00 still plays at
// 01:00 on Saturday morning.
func inWindow(start, end string, now time.Time, days []string) bool {
	if start == "" && end == "" {
		return dayPermitted(days, now.Weekday())
	}
	// config.ParseClock is the one clock format of the product. The scheduler had
	// a parser of its own, and it took values that the validator refused.
	s, ok := config.ParseClock(start)
	if !ok {
		return false
	}
	e, ok := config.ParseClock(end)
	if !ok {
		return false
	}
	if s == e {
		return false // a window of no length matches nothing
	}
	minute := now.Hour()*60 + now.Minute()
	if s < e {
		return minute >= s && minute < e && dayPermitted(days, now.Weekday())
	}
	// The window goes past midnight.
	if minute >= s {
		return dayPermitted(days, now.Weekday())
	}
	if minute < e {
		return dayPermitted(days, previousDay(now.Weekday()))
	}
	return false
}

// dayPermitted reports if a day is in the list. An empty list means every day.
func dayPermitted(days []string, day time.Weekday) bool {
	if len(days) == 0 {
		return true
	}
	return slices.Contains(days, dayNames[int(day)])
}

func previousDay(day time.Weekday) time.Weekday {
	return time.Weekday((int(day) + 6) % 7)
}
