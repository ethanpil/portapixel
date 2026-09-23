package manifest

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// snakeCase is the shape that every JSON name in this package must have.
var snakeCase = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func TestJSONNamesAreSnakeCase(t *testing.T) {
	types := []any{
		EnrollRequest{}, EnrollResponse{}, Manifest{}, Playlist{}, Item{},
		Rule{}, MediaRef{}, Command{}, ScreenRule{}, ReleaseRef{}, Heartbeat{},
		Status{}, NowPlaying{}, UpdateState{},
	}
	for _, v := range types {
		rt := reflect.TypeOf(v)
		t.Run(rt.Name(), func(t *testing.T) {
			for i := range rt.NumField() {
				f := rt.Field(i)
				tag := f.Tag.Get("json")
				if tag == "" {
					t.Errorf("field %s has no json tag", f.Name)
					continue
				}
				name := strings.Split(tag, ",")[0]
				if !snakeCase.MatchString(name) {
					t.Errorf("field %s has json name %q, which is not snake_case", f.Name, name)
				}
			}
		})
	}
}

func TestStatusKeys(t *testing.T) {
	want := []string{
		"device_id", "name", "mdns_name", "ips", "version", "image_version",
		"package_manifest_hash", "arch", "tier", "uptime_seconds", "load",
		"temp_c", "ram_total_bytes", "ram_free_bytes", "media_total_bytes",
		"media_free_bytes", "browser_state", "navigation_rung",
		"display_connected", "screen_on", "paired", "server_url", "last_sync",
		"last_sync_result", "clock_synced", "timezone", "warnings",
		"hardware_changed", "config_from_shadow", "update",
	}
	data, err := json.Marshal(Status{})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			t.Errorf("key %q is missing from an empty Status", key)
		}
	}
	// These keys must stay out of an empty Status.
	for _, key := range []string{"now_playing", "pairing_code", "sync_error", "codecs"} {
		if _, ok := got[key]; ok {
			t.Errorf("key %q must be left out when it is empty", key)
		}
	}
	if len(got) != len(want) {
		t.Errorf("an empty Status has %d keys, want %d: %v", len(got), len(want), got)
	}
}

func TestRoundTrip(t *testing.T) {
	shuffle := true
	tests := []struct {
		name  string
		value any
	}{
		{
			name: "manifest",
			value: Manifest{
				ServerName:      "site-1",
				PollSeconds:     60,
				DefaultPlaylist: "lobby",
				Playlists: []Playlist{{
					Name:       "lobby",
					Title:      "Lobby loop",
					Transition: "cut",
					Shuffle:    &shuffle,
					Items: []Item{
						{SHA256: "aa", Duration: 15},
						{URL: "https://example.com/board", Duration: 60, RefreshSeconds: 300},
					},
				}},
				Schedule: []Rule{{Playlist: "lobby", Days: []string{"mon"}, Start: "08:00", End: "18:00"}},
				Media:    []MediaRef{{SHA256: "aa", Size: 12, Name: "a.jpg", URL: "/api/v1/media/aa"}},
				Commands: []Command{{ID: 7, Type: "reboot", Args: map[string]string{"why": "test"}}},
				Screen:   &ScreenRule{OnTime: "07:30", OffTime: "22:00", Days: []string{"mon"}},
				Release:  &ReleaseRef{Version: "1.2.3", BaseURL: "/api/v1/releases/1.2.3"},
			},
		},
		{
			name: "heartbeat",
			value: Heartbeat{
				DeviceID:   "px-12345678",
				HardwareID: strings.Repeat("a", 64),
				Name:       "Lobby",
				Version:    "1.2.3",
				Acks:       []int64{1, 2},
				SyncError:  "needs 4.2 GB, has 1.1 GB",
				Status: Status{
					DeviceID:   "px-12345678",
					IPs:        []string{"192.168.1.5"},
					LastSync:   time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
					Warnings:   []Warning{{Code: WarnWebPassword, Message: "change the web password"}},
					NowPlaying: &NowPlaying{Playlist: "lobby", Index: 2, Item: "a.jpg", Kind: "image", SHA256: strings.Repeat("b", 64)},
					Update:     UpdateState{State: "idle"},
				},
			},
		},
		{
			name:  "enroll request",
			value: EnrollRequest{DeviceID: "px-12345678", HardwareID: "ab", Name: "Lobby", Token: "t", Version: "dev"},
		},
		{
			name:  "enroll response",
			value: EnrollResponse{Status: "pending", PairingCode: "K7M9QX", ClaimSecret: "s"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			out := reflect.New(reflect.TypeOf(tt.value))
			if err := json.Unmarshal(data, out.Interface()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(out.Elem().Interface(), tt.value) {
				t.Fatalf("round trip changed the value:\n got %+v\nwant %+v", out.Elem().Interface(), tt.value)
			}
		})
	}
}

// The server finds the library object of the item on a screen by its hash. The
// name on the device is the object name, which is not the name in the library.
func TestNowPlayingCarriesTheHash(t *testing.T) {
	data, err := json.Marshal(NowPlaying{Item: "55efb67e-lab2-teal.png", Kind: "image", SHA256: "55efb67e"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"sha256":"55efb67e"`) {
		t.Errorf("now_playing is %s, want a sha256 key", data)
	}
	data, err = json.Marshal(NowPlaying{Item: "https://example.com", Kind: "url"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sha256") {
		t.Errorf("a URL item has no hash, but now_playing is %s", data)
	}
}

func TestCleanName(t *testing.T) {
	tests := []struct {
		raw  string
		want string
		ok   bool
	}{
		{raw: "Lobby north", want: "Lobby north", ok: true},
		{raw: "  Front desk \t", want: "Front desk", ok: true},
		{raw: "Écran du hall", want: "Écran du hall", ok: true},
		{raw: strings.Repeat("é", MaxNameLength), want: strings.Repeat("é", MaxNameLength), ok: true},
		{raw: strings.Repeat("a", MaxNameLength+1)},
		{raw: ""},
		{raw: "   "},
		{raw: "two\nlines"},
		{raw: "a\x00b"},
		{raw: "del\x7f"},
		{raw: "c1\u0085x"},
		{raw: "bad \xff utf-8"},
	}
	for _, tt := range tests {
		got, ok := CleanName(tt.raw)
		if got != tt.want || ok != tt.ok {
			t.Errorf("CleanName(%q) = %q, %v; want %q, %v", tt.raw, got, ok, tt.want, tt.ok)
		}
	}
}

// The mDNS name comes from the display name, and a DNS label holds 63 octets. A
// name of 64 characters gave an announcement that answered no query.
func TestCleanNameFitsOneDNSLabel(t *testing.T) {
	if _, ok := CleanName(strings.Repeat("a", 63)); !ok {
		t.Error("CleanName refused a name of 63 characters")
	}
	if _, ok := CleanName(strings.Repeat("a", 64)); ok {
		t.Error("CleanName took a name of 64 characters, which is too long for a DNS label")
	}
}

// A name that a person cannot see, or that turns the text beside it around, is
// refused. ZWNJ and ZWJ stay: Persian, the Indic scripts and emoji need them.
func TestCleanNameRefusesInvisibleCharacters(t *testing.T) {
	for _, good := range []string{
		"\u0645\u06CC\u200C\u062E\u0648\u0627\u0647\u0645", // ZWNJ in Persian
		"\u0915\u094D\u200D\u0937",                         // ZWJ in Devanagari
		"Team \U0001F469\u200D\U0001F4BB desk",             // ZWJ in an emoji
	} {
		if got, ok := CleanName(good); !ok || got != good {
			t.Errorf("CleanName(%q) = %q, %v; want the same name, true", good, got, ok)
		}
	}
	for _, r := range []rune{
		'\u2028', '\u2029', // Zl, Zp
		'\u200E', '\u200F', '\u202A', '\u202B', '\u202C', '\u202D', '\u202E',
		'\u2066', '\u2067', '\u2068', '\u2069', // bidi controls
		'\u200B', '\u2060', '\uFEFF', '\u00AD', // invisible
	} {
		raw := "Lobby" + string(r) + "north"
		if got, ok := CleanName(raw); ok {
			t.Errorf("CleanName took U+%04X and gave %q", r, got)
		}
	}
}
