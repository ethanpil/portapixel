package audio

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/opslog"
)

// The card list of an x86 machine with HDMI, an analogue codec and a USB speaker.
const pcCards = ` 0 [HDMI           ]: HDA-Intel - HDA Intel HDMI
                      HDA Intel HDMI at 0xf7e34000 irq 33
 1 [PCH            ]: HDA-Intel - HDA Intel PCH
                      HDA Intel PCH at 0xf7e30000 irq 32
 2 [Device         ]: USB-Audio - USB PnP Sound Device
                      USB PnP Sound Device at usb-0000:00:14.0-2, full speed
`

// The card list of a Raspberry Pi 4: two HDMI outputs and the headphone jack.
const piCards = ` 0 [vc4hdmi0       ]: vc4-hdmi - vc4-hdmi-0
                      vc4-hdmi-0
 1 [vc4hdmi1       ]: vc4-hdmi - vc4-hdmi-1
                      vc4-hdmi-1
 2 [Headphones     ]: bcm2835_headpho - bcm2835 Headphones
                      bcm2835 Headphones
`

// call is one program call that the fake runner took.
type call struct {
	name string
	args []string
}

// runner is a fake Runner. fail names the controls that refuse the call.
type runner struct {
	mu    sync.Mutex
	calls []call
	fail  map[string]bool
	out   string
}

func (r *runner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call{name: name, args: args})
	control := ""
	if len(args) >= 3 {
		control = args[2]
	}
	if r.fail[control] {
		return []byte(r.out), errors.New("exit status 1")
	}
	return nil, nil
}

func (r *runner) taken() []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// fixture makes an Applier with a card list on disk and a fake runner.
type fixture struct {
	a    *Applier
	run  *runner
	conf string
	log  *opslog.Log
}

func newFixture(t *testing.T, cards string) *fixture {
	t.Helper()
	dir := t.TempDir()
	cardsFile := filepath.Join(dir, "cards")
	if err := os.WriteFile(cardsFile, []byte(cards), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		run:  &runner{fail: map[string]bool{}},
		conf: filepath.Join(dir, "asound.conf"),
		log:  opslog.New(filepath.Join(dir, "ops.log")),
	}
	f.a = New(Options{
		Run:       f.run,
		CardsFile: cardsFile,
		ConfPath:  f.conf,
		Log:       f.log,
		// The tests run on Windows as well, so the Linux gate is a fake.
		Linux: func() bool { return true },
	})
	return f
}

func (f *fixture) confText(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.conf)
	if err != nil {
		return ""
	}
	return string(data)
}

func (f *fixture) events() string {
	var b strings.Builder
	for _, e := range f.log.Tail(100) {
		b.WriteString(e.Event + " " + e.Details + "\n")
	}
	return b.String()
}

// The output names a card by a name pattern. The card number in /etc/asound.conf
// is what ALSA reads, so the number is the whole answer.
func TestOutputSelectsTheCard(t *testing.T) {
	tests := []struct {
		name   string
		cards  string
		output string
		// want is the card number, or -1 when no file must exist.
		want int
	}{
		{name: "PC, hdmi", cards: pcCards, output: OutputHDMI, want: 0},
		{name: "PC, analog", cards: pcCards, output: OutputAnalog, want: 1},
		{name: "PC, usb", cards: pcCards, output: OutputUSB, want: 2},
		{name: "PC, auto writes no file", cards: pcCards, output: OutputAuto, want: -1},
		{name: "Pi, hdmi takes the first HDMI card", cards: piCards, output: OutputHDMI, want: 0},
		{name: "Pi, analog is the headphone jack", cards: piCards, output: OutputAnalog, want: 2},
		{name: "Pi has no USB card", cards: piCards, output: OutputUSB, want: -1},
		{name: "no card list at all", cards: "", output: OutputHDMI, want: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.cards)
			f.a.Apply(config.Audio{Output: tt.output, Volume: 100})

			text := f.confText(t)
			if tt.want < 0 {
				if text != "" {
					t.Fatalf("a file was written:\n%s", text)
				}
				return
			}
			if !strings.HasPrefix(text, header) {
				t.Fatalf("the file has no header line:\n%s", text)
			}
			for _, want := range []string{
				"defaults.pcm.card " + strconv.Itoa(tt.want),
				"defaults.ctl.card " + strconv.Itoa(tt.want),
			} {
				if !strings.Contains(text, want+"\n") {
					t.Fatalf("the file has no line %q:\n%s", want, text)
				}
			}
		})
	}
}

// The output "auto" leaves the default of ALSA. A file that this package wrote
// goes away, and a file that a person wrote stays: a device must never delete the
// work of a person.
func TestAutoRemovesOnlyOurFile(t *testing.T) {
	f := newFixture(t, pcCards)
	f.a.Apply(config.Audio{Output: OutputHDMI, Volume: 50})
	if f.confText(t) == "" {
		t.Fatal("hdmi wrote no file")
	}
	f.a.Apply(config.Audio{Output: OutputAuto, Volume: 50})
	if text := f.confText(t); text != "" {
		t.Fatalf("auto kept our file:\n%s", text)
	}

	own := "pcm.!default { type hw card 3 }\n"
	if err := os.WriteFile(f.conf, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	f.a.Apply(config.Audio{Output: OutputAuto, Volume: 50})
	if text := f.confText(t); text != own {
		t.Fatalf("the file of the person is %q", text)
	}
}

// The volume goes to Master, and to PCM when the card has no Master control.
func TestVolumeTriesMasterThenPCM(t *testing.T) {
	tests := []struct {
		name string
		fail map[string]bool
		// want is the control of each call, in order.
		want []string
		// warn says that Error must hold a sentence.
		warn bool
	}{
		{name: "Master answers", want: []string{"Master"}},
		{name: "Master refuses, PCM answers", fail: map[string]bool{"Master": true}, want: []string{"Master", "PCM"}},
		{
			name: "both refuse",
			fail: map[string]bool{"Master": true, "PCM": true},
			want: []string{"Master", "PCM"},
			warn: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, pcCards)
			f.run.fail = tt.fail
			f.run.out = "amixer: Unable to find simple control\n"
			f.a.Apply(config.Audio{Output: OutputAuto, Volume: 65})

			calls := f.run.taken()
			if len(calls) != len(tt.want) {
				t.Fatalf("calls = %+v, want %d", calls, len(tt.want))
			}
			for i, c := range calls {
				if c.name != mixerTool {
					t.Fatalf("call %d ran %q", i, c.name)
				}
				want := []string{"-q", "sset", tt.want[i], "65%"}
				if strings.Join(c.args, " ") != strings.Join(want, " ") {
					t.Fatalf("call %d = %v, want %v", i, c.args, want)
				}
			}
			if warned := f.a.Error() != ""; warned != tt.warn {
				t.Fatalf("Error() = %q, want a warning: %v", f.a.Error(), tt.warn)
			}
			if tt.warn && !strings.Contains(f.events(), "audio.volume.fail") {
				t.Fatalf("the fault is not in the ops log:\n%s", f.events())
			}
		})
	}
}

// A fault that does not change is one line in the ops log. The daemon applies the
// configuration at every save and at every hand edit of portapixel.toml, and a log
// that holds one fault a hundred times holds nothing else.
func TestTheSameFaultIsLoggedOneTime(t *testing.T) {
	f := newFixture(t, pcCards)
	f.run.fail = map[string]bool{"Master": true, "PCM": true}
	for i := 0; i < 4; i++ {
		f.a.Apply(config.Audio{Output: OutputAuto, Volume: 30})
	}
	if n := strings.Count(f.events(), "audio.volume.fail"); n != 1 {
		t.Fatalf("the fault is in the log %d times:\n%s", n, f.events())
	}

	// A mixer that answers again clears the warning.
	f.run.fail = map[string]bool{}
	f.a.Apply(config.Audio{Output: OutputAuto, Volume: 30})
	if got := f.a.Error(); got != "" {
		t.Fatalf("Error() = %q after a good call", got)
	}
}

// A machine that is not Linux has no ALSA. Apply must do nothing at all there:
// the development machine is Windows.
func TestApplyDoesNothingWithoutALSA(t *testing.T) {
	f := newFixture(t, pcCards)
	f.a = New(Options{
		Run:       f.run,
		CardsFile: filepath.Join(t.TempDir(), "cards"),
		ConfPath:  f.conf,
		Log:       f.log,
		Linux:     func() bool { return false },
	})
	f.a.Apply(config.Audio{Output: OutputHDMI, Volume: 10})
	if calls := f.run.taken(); len(calls) != 0 {
		t.Fatalf("calls = %+v", calls)
	}
	if text := f.confText(t); text != "" {
		t.Fatalf("a file was written:\n%s", text)
	}
}

// The volume of the configuration is 0 to 100. A value from outside that range
// can only come from a caller with a bug, and a percentage that ALSA refuses
// would look like a broken mixer.
func TestVolumeIsCappedAtTheRange(t *testing.T) {
	tests := []struct {
		volume int
		want   string
	}{
		{volume: -20, want: "0%"},
		{volume: 0, want: "0%"},
		{volume: 100, want: "100%"},
		{volume: 400, want: "100%"},
	}
	for _, tt := range tests {
		f := newFixture(t, pcCards)
		f.a.Apply(config.Audio{Output: OutputAuto, Volume: tt.volume})
		calls := f.run.taken()
		if len(calls) != 1 || calls[0].args[3] != tt.want {
			t.Fatalf("volume %d gave %+v, want %q", tt.volume, calls, tt.want)
		}
	}
}

func TestParseCards(t *testing.T) {
	got := parseCards(pcCards)
	if len(got) != 3 {
		t.Fatalf("cards = %+v", got)
	}
	if got[2].index != 2 || !holds(got[2].text, "usb") {
		t.Fatalf("card 2 = %+v", got[2])
	}
	// The second line of a card belongs to that card.
	if !holds(got[0].text, "0xf7e34000") {
		t.Fatalf("card 0 lost its second line: %+v", got[0])
	}
	// A card number of two digits starts at the left margin.
	two := parseCards("10 [Loopback      ]: Loopback - Loopback\n")
	if len(two) != 1 || two[0].index != 10 {
		t.Fatalf("cards = %+v", two)
	}
	if parseCards("") != nil {
		t.Fatal("an empty file gave a card")
	}
}
