package syncer

import (
	"context"
	"fmt"
	"strconv"

	"github.com/ethanpil/portapixel/internal/device/identity"
	"github.com/ethanpil/portapixel/internal/manifest"
)

// MaxAcks is the largest number of command acknowledgements that one heartbeat may
// carry. The server refuses a heartbeat with more, and a refused heartbeat loses
// every acknowledgement in it. So a round that ran more commands than this
// acknowledges the first MaxAcks of them and the rest at the next poll.
const MaxAcks = 100

// The command types of the fleet queue (ARCHITECTURE section 5).
const (
	CmdReboot         = "reboot"
	CmdRestartBrowser = "restart-browser"
	CmdScreenOn       = "screen-on"
	CmdScreenOff      = "screen-off"
	CmdRescan         = "rescan"
	CmdUpdate         = "update"
	CmdRename         = "rename"
)

// runCommands runs each command one time and gives the IDs to acknowledge.
//
// The server sends a command again when no acknowledgement arrives inside ten
// minutes, up to three times. So an ID that already ran is acknowledged again and
// never run a second time: a reboot that the acknowledgement missed would otherwise
// restart the screen three times.
//
// rebooting is true when a reboot command ran. The caller then stops: the
// acknowledgement went out already, and the machine is going down.
//
// A command that can end this process is acknowledged before it runs. See
// endsTheProcess.
func (s *Syncer) runCommands(ctx context.Context, base, token string, commands []manifest.Command) (acks []int64, rebooting bool) {
	for _, c := range commands {
		if s.opt.State().RanCommand(c.ID) {
			// A repeat. The acknowledgement is what the server waits for.
			acks = append(acks, c.ID)
			continue
		}

		// A command whose work can end this process is recorded and acknowledged
		// BEFORE it runs. A reboot goes down; an update restarts the service. An ID
		// that was not written is an ID that the server sends again, up to three
		// times. The screen then reboots three times, or it attempts a release that
		// already failed.
		if endsTheProcess(c.Type) {
			s.markCommand(c.ID)
			ackCtx, cancel := context.WithTimeout(context.Background(), ackTimeout)
			err := s.heartbeat(ackCtx, base, token, append(acks, c.ID))
			cancel()
			if err != nil {
				s.log("sync.command.ack.fail", c.Type+" goes on without an acknowledgement: "+err.Error())
			}
			acks = append(acks, c.ID)
			if c.Type == CmdReboot {
				s.log("sync.command", CmdReboot)
				s.runLocal(CmdReboot)
				return acks, true
			}
			// An update that installs restarts the service, and one that finds
			// nothing lets the round go on to the next command.
			s.execute(ctx, c)
			continue
		}

		s.execute(ctx, c)
		s.markCommand(c.ID)
		acks = append(acks, c.ID)
	}
	return acks, false
}

// endsTheProcess reports if the work of a command can stop this process before it
// can write anything. Such a command is acknowledged first.
func endsTheProcess(kind string) bool { return kind == CmdReboot || kind == CmdUpdate }

// execute does the work of one command. An unknown type is acknowledged with an
// ops log line: a server that knows a command that this release does not must never
// stop the device.
func (s *Syncer) execute(ctx context.Context, c manifest.Command) {
	switch c.Type {
	case CmdRestartBrowser, CmdScreenOn, CmdScreenOff, CmdRescan:
		s.log("sync.command", c.Type)
		s.runLocal(c.Type)
	case CmdUpdate:
		s.log("sync.command", c.Type)
		if s.opt.Update == nil {
			return
		}
		if err := s.opt.Update(ctx); err != nil {
			// No approved release, or a release that this device runs already. The
			// command is done either way: the server asked, and the device answered.
			s.log("sync.command.update", err.Error())
		}
	case CmdRename:
		s.rename(c.Args["name"])
	default:
		s.log("sync.command.unknown", "the server asked for "+c.Type+", which this release does not know")
	}
}

// rename saves the display name that the server sent. The device owns its name:
// mDNS, the host name and the fallback screen use it. So the server does not
// change it directly. It sends this command, and the next heartbeat reports the
// new name.
//
// A name that the device cannot use is acknowledged with a line in the ops log.
// The server sends a command again only when no acknowledgement arrives, so a
// refusal must never stop the round. The log line does not quote the refused
// value, because it can hold a control character.
func (s *Syncer) rename(raw string) {
	name, ok := manifest.CleanName(raw)
	if !ok {
		s.log("sync.command.rename", fmt.Sprintf(
			"the server sent a name that this device cannot use (1 to %d characters, no control character); the name did not change",
			manifest.MaxNameLength))
		return
	}
	if s.opt.SaveName == nil {
		return
	}
	if err := s.opt.SaveName(name); err != nil {
		s.log("sync.command.rename", "the device cannot save the name "+strconv.Quote(name)+": "+err.Error())
		return
	}
	s.log("sync.command", CmdRename+" to "+strconv.Quote(name))
}

// runLocal calls the local command of the daemon and logs a refusal.
func (s *Syncer) runLocal(name string) {
	if s.opt.Command == nil {
		return
	}
	if err := s.opt.Command(name); err != nil {
		s.log("sync.command.fail", name+": "+err.Error())
	}
}

// markCommand writes the ID of a command that ran into the state file. The write is
// staged and committed with a rename, so a power cut cannot lose it (D41).
func (s *Syncer) markCommand(id int64) {
	if err := s.opt.SaveState(func(st *identity.State) { st.MarkCommand(id) }); err != nil {
		s.log("sync.state.write.fail", err.Error())
	}
}
