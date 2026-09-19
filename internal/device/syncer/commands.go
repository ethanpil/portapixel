package syncer

import (
	"context"

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
func (s *Syncer) runCommands(ctx context.Context, base, token string, commands []manifest.Command) (acks []int64, rebooting bool) {
	for _, c := range commands {
		if s.opt.State().RanCommand(c.ID) {
			// A repeat. The acknowledgement is what the server waits for.
			acks = append(acks, c.ID)
			continue
		}

		if c.Type == CmdReboot {
			// The ID and the acknowledgement go out before the machine goes down.
			// Without that the server sends the command again and the screen
			// reboots three times.
			s.markCommand(c.ID)
			ackCtx, cancel := context.WithTimeout(context.Background(), ackTimeout)
			err := s.heartbeat(ackCtx, base, token, append(acks, c.ID))
			cancel()
			if err != nil {
				s.log("sync.command.ack.fail", "the reboot goes on without an acknowledgement: "+err.Error())
			}
			s.log("sync.command", "reboot")
			s.runLocal(CmdReboot)
			return append(acks, c.ID), true
		}

		s.execute(ctx, c)
		s.markCommand(c.ID)
		acks = append(acks, c.ID)
	}
	return acks, false
}

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
	default:
		s.log("sync.command.unknown", "the server asked for "+c.Type+", which this release does not know")
	}
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
