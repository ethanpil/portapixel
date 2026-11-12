// Package priority gives the daemon and the player their share of the machine
// (the setting playback.priority).
//
// Why this package exists: on an on-box install, and on a small board, other
// work runs beside the player: a backup, a cron job, a container. A picture that
// stutters is a fault that a person sees. The kernel gives each process the same
// share unless somebody says otherwise.
//
// "high" applies four things to the daemon at its start. The daemon runs as root
// and starts mpv, so mpv inherits all four, also through the change to the
// kiosk account: nice, the I/O priority and oom_score_adj survive setuid and
// exec, and a child stays in the cgroup of its parent.
//
//  1. nice -5 (setpriority). On Linux the nice value belongs to a thread, so
//     the daemon sets it on each thread in /proc/self/task. A new thread
//     inherits it from the thread that makes it.
//  2. The I/O class best-effort, level 0 (ioprio_set), also on each thread.
//  3. oom_score_adj -500. The value belongs to the process. A root process that
//     writes it also sets the lower limit, so mpv, which is not root, cannot go
//     lower, and it inherits -500. mpv uses much more memory than the daemon,
//     so the OOM killer still takes mpv before the daemon, and the daemon starts
//     it again.
//  4. cpu.weight 1000 (the default is 100) on the cgroup v2 of the service.
//     OpenRC puts each service in /sys/fs/cgroup/openrc.<service> when
//     rc_cgroup_mode is "unified" and the cgroups service runs (os/install.sh
//     sets both). The daemon writes the weight only to a cgroup of its own,
//     never to the root cgroup "/", and only when the cpu controller is on
//     there. If not, it applies 1 to 3 and reports the status warning
//     priority-cgroup-unavailable.
//
// "normal" applies nothing. No real-time class: a player that spins in a
// real-time class can take the whole machine, the SSH login too.
//
// The calls are for Linux only. Away from Linux, Host gives no System and
// Apply does nothing. The tests use a fake System.
package priority
