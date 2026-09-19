// Package scheduler says which playlist must play now, and when the screen must
// be on.
//
// Why this package exists: two schedules read the same clock and the same day
// names. Both must hold still until the clock is true. A Raspberry Pi has no
// battery clock. It can wake up in 1970 or, with swclock, at the time of the last
// shutdown. A rule that plays the sale loop from 08:00 to 18:00 then shows the
// wrong content. So the rules stay inert and the default playlist plays until the
// kernel reports a clock with a source (D17, D40).
//
// The rules of the two schedules:
//
//   - Playlist rules: the first rule that matches wins. A rule with no days
//     matches every day. A rule whose end is before its start goes past
//     midnight, and the day list then names the day on which the rule starts.
//   - Screen schedule: the screen is on between on_time and off_time, with the
//     same midnight rule. With no times set, the screen is always on. With
//     power_days set, a day that is not in the list has no on period at all.
//
// While the device is paired, the rules and the default playlist come from the
// fleet server (D48). SetFleetRules gives them to the scheduler.
//
// The clock, the sync probe and the configuration are all parameters. The tests
// then need no real clock, no chrony and no configuration file.
package scheduler
