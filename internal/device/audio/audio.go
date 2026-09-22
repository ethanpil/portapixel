package audio

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/opslog"
)

// The words of audio.output.
const (
	OutputAuto   = "auto"
	OutputHDMI   = "hdmi"
	OutputAnalog = "analog"
	OutputUSB    = "usb"
)

// mixerTool is the mixer program of alsa-utils.
const mixerTool = "amixer"

// mixerControls are the two mixer controls that a card can have, in the order to
// try them. Master is the control of most cards. Many HDMI and USB codecs have no
// Master control, and PCM is then the one that carries the volume.
var mixerControls = []string{"Master", "PCM"}

// commandTimeout is how long one amixer call may take. The program talks to a
// local device and answers at once.
const commandTimeout = 10 * time.Second

// DefaultCardsFile is where the kernel lists the sound cards. DefaultConfPath is
// the ALSA file that names the default card.
const (
	DefaultCardsFile = "/proc/asound/cards"
	DefaultConfPath  = "/etc/asound.conf"
)

// header is the first line of the file that this package writes.
//
// It is also the permission to remove that file. The output "auto" leaves no
// file, and a person can have written an /etc/asound.conf of their own. A device
// must never delete such a file, so only a file that starts with this line goes
// away.
const header = "# PortaPixel writes this file from [audio] in portapixel.toml.\n"

// Runner runs one program and gives its output. The daemon gives a runner that
// starts the program as root; a test gives a fake, so nothing here needs Linux, a
// sound card or root rights.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Options are the parameters of an Applier.
type Options struct {
	// Run runs amixer.
	Run Runner
	// CardsFile lists the sound cards. "" uses DefaultCardsFile.
	CardsFile string
	// ConfPath is the ALSA file that names the default card. "" uses
	// DefaultConfPath.
	ConfPath string
	Log      *opslog.Log
	// Linux reports if this machine has ALSA. A nil function answers for the
	// operating system that the binary runs on.
	Linux func() bool
}

// Applier puts the [audio] table into effect. It is safe for use by more than one
// goroutine.
type Applier struct {
	opt Options

	// applyMu holds one Apply at a time. Two applies that ran together could
	// write the file and set the mixer in two orders.
	applyMu sync.Mutex

	mu sync.Mutex
	// lastError is the sentence of the status warning, or "".
	lastError string
	// said is the fault that the ops log already holds. A fault that does not
	// change is written one time: the daemon applies the configuration at every
	// save and at every hand edit.
	said string
}

// New makes an Applier. It talks to nothing: the caller calls Apply.
func New(opt Options) *Applier {
	if opt.CardsFile == "" {
		opt.CardsFile = DefaultCardsFile
	}
	if opt.ConfPath == "" {
		opt.ConfPath = DefaultConfPath
	}
	if opt.Linux == nil {
		opt.Linux = func() bool { return runtime.GOOS == "linux" }
	}
	return &Applier{opt: opt}
}

// Apply puts the output and the volume into effect. It gives no error: a device
// that cannot set its volume still plays a picture, and the fault is in the ops
// log and in Error().
//
// The order is the file first and the mixer second. The file must be on the disk
// before a new browser process starts, because Chromium reads the default ALSA
// device at its start. The mixer call runs a program, so it is the slow half.
//
// The daemon calls this in a goroutine at start, so that a mixer that does not
// answer cannot hold up the first picture.
func (a *Applier) Apply(cfg config.Audio) {
	if !a.opt.Linux() {
		return
	}
	a.applyMu.Lock()
	defer a.applyMu.Unlock()

	a.applyOutput(cfg.Output)
	a.applyVolume(cfg.Volume)
}

// Error gives the sentence of the last fault, or "". The health report turns it
// into the warning audio-apply-failed.
func (a *Applier) Error() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastError
}

// applyOutput writes the ALSA file that names the default card.
func (a *Applier) applyOutput(output string) {
	if output == "" || output == OutputAuto {
		a.removeConf()
		return
	}
	index, ok := selectCard(parseCards(readFile(a.opt.CardsFile)), output)
	if !ok {
		a.log("audio.output.nocard", "no sound card matches "+output+"; ALSA keeps its own default")
		return
	}
	data := header +
		"# A save of the settings writes it again. Your own lines do not come back.\n" +
		"defaults.pcm.card " + strconv.Itoa(index) + "\n" +
		"defaults.ctl.card " + strconv.Itoa(index) + "\n"
	if err := fsutil.WriteFileAtomic(a.opt.ConfPath, []byte(data), 0o644); err != nil {
		a.log("audio.output.fail", err.Error())
		return
	}
	a.log("audio.output", output+" is card "+strconv.Itoa(index))
}

// removeConf takes away a file that this package wrote. A file with another first
// line belongs to a person and stays.
func (a *Applier) removeConf() {
	data := readFile(a.opt.ConfPath)
	if data == "" || !strings.HasPrefix(data, header) {
		return
	}
	if err := os.Remove(a.opt.ConfPath); err != nil {
		a.log("audio.output.fail", err.Error())
		return
	}
	a.log("audio.output", "auto: ALSA chooses the card")
}

// applyVolume sets the mixer. It tries Master and then PCM: a card with no Master
// control refuses the first call, and that is not a fault of the device.
func (a *Applier) applyVolume(volume int) {
	if a.opt.Run == nil {
		return
	}
	if volume < 0 {
		volume = 0
	}
	if volume > 100 {
		volume = 100
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	value := strconv.Itoa(volume) + "%"
	last := ""
	for _, control := range mixerControls {
		out, err := a.opt.Run.Run(ctx, mixerTool, mixerArgs(control, value)...)
		if err == nil {
			a.clearFault()
			return
		}
		last = control + ": " + faultText(out, err)
	}
	a.fault("The volume is not set. " + mixerTool + " refused " +
		strings.Join(mixerControls, " and ") + " (" + last + ").")
}

// mixerArgs builds the argument list of one amixer call. It is here and nowhere
// else.
//
//	amixer -q sset Master 60%
//
// -q keeps the output short: the tool prints the whole control on success.
func mixerArgs(control, value string) []string {
	return []string{"-q", "sset", control, value}
}

// fault records a fault and writes one ops log line for it.
func (a *Applier) fault(message string) {
	a.mu.Lock()
	a.lastError = message
	first := a.said != message
	a.said = message
	a.mu.Unlock()

	if first {
		a.log("audio.volume.fail", message)
	}
}

// clearFault forgets a fault that is over.
func (a *Applier) clearFault() {
	a.mu.Lock()
	had := a.lastError != ""
	a.lastError = ""
	a.said = ""
	a.mu.Unlock()

	if had {
		a.log("audio.volume", "the mixer takes the volume again")
	}
}

// card is one sound card of /proc/asound/cards: its number and the text that
// names it.
type card struct {
	index int
	text  string
}

// parseCards reads the card list of the kernel. The file holds two lines for each
// card:
//
//	0 [HDMI           ]: HDA-Intel - HDA Intel HDMI
//	                     HDA Intel HDMI at 0xf7e34000 irq 33
//
// A card starts with its number and the identifier in square brackets. Every
// other line belongs to the card above it, and its words count as part of the
// name: one driver names the output on the first line and another names it on the
// second.
func parseCards(text string) []card {
	var out []card
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		number, rest, found := strings.Cut(trimmed, " ")
		index, err := strconv.Atoi(number)
		if found && err == nil && strings.HasPrefix(strings.TrimLeft(rest, " "), "[") {
			out = append(out, card{index: index, text: rest})
			continue
		}
		if len(out) > 0 {
			out[len(out)-1].text += " " + trimmed
		}
	}
	return out
}

// selectCard gives the number of the card that audio.output names.
func selectCard(cards []card, output string) (int, bool) {
	for _, c := range cards {
		hdmi, usb := holds(c.text, "hdmi"), holds(c.text, "usb")
		switch output {
		case OutputHDMI:
			if hdmi {
				return c.index, true
			}
		case OutputUSB:
			if usb {
				return c.index, true
			}
		case OutputAnalog:
			// The headphone jack of a Raspberry Pi and the analogue output of a PC
			// codec have no one name. Everything that is not HDMI and not USB is the
			// analogue card.
			if !hdmi && !usb {
				return c.index, true
			}
		}
	}
	return 0, false
}

// holds reports if the text of a card names this word, in any case.
func holds(text, word string) bool {
	return strings.Contains(strings.ToLower(text), word)
}

// faultText makes one line out of the output and the error of a program. A tool
// that fails says why on its output, and an exit status on its own teaches
// nobody.
func faultText(out []byte, err error) string {
	text := strings.TrimSpace(string(out))
	if at := strings.IndexByte(text, '\n'); at > 0 {
		text = text[:at]
	}
	if text == "" {
		return err.Error()
	}
	return text
}

func (a *Applier) log(event, details string) {
	if a.opt.Log != nil {
		a.opt.Log.Log(event, details)
	}
}

// readFile reads a small file and gives "" when it cannot.
func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
