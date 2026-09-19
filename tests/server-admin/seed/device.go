package main

import (
	"fmt"
	"time"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// heartbeatFor builds the report of one fake screen. Each screen reports a
// different health picture, so the fleet list shows every colour.
func heartbeatFor(s screen) manifest.Heartbeat {
	now := time.Now().UTC()
	st := manifest.Status{
		DeviceID: s.id, Name: s.name, MDNSName: slug(s.name) + ".local",
		IPs:     []string{"10.4.2." + fmt.Sprint(60+len(s.id)%40)},
		Version: s.version, Arch: "arm64", Tier: "high",
		UptimeSeconds: 96 * 3600, Load: 0.31, TempC: 47.5,
		RAMTotalBytes: 2 << 30, RAMFreeBytes: 1 << 30,
		MediaTotalBytes: 58 << 30, MediaFreeBytes: 31 << 30,
		BrowserState: "running", DisplayConnected: true, ScreenOn: true,
		Paired: true, ServerURL: "http://127.0.0.1:8095",
		LastSync: now.Add(-40 * time.Second), LastSyncResult: "ok",
		ClockSynced: true, Timezone: "America/New_York",
		Codecs: codecsFor("high"),
		Update: manifest.UpdateState{State: "idle", Current: s.version},
	}
	hb := manifest.Heartbeat{
		DeviceID: s.id, HardwareID: s.hardware, Name: s.name, Version: s.version, Status: st,
	}

	switch s.id {
	case "px-4a91c7e2": // Lobby North: everything is well.
		st.NowPlaying = &manifest.NowPlaying{
			Playlist: "lobby-loop", Index: 1, Item: "welcome-autumn.jpg", Kind: "image",
			Since: now.Add(-8 * time.Second),
		}
		st.TempC = 44.2
	case "px-7c33b190": // Cafeteria West: warm, and it goes quiet after this report.
		st.NowPlaying = &manifest.NowPlaying{
			Playlist: "menu-boards", Index: 0, Item: "menu-board.png", Kind: "image",
			Since: now.Add(-3 * time.Minute),
		}
		st.TempC = 68.9
		st.MediaFreeBytes = 9 << 30
	case "px-5e10d8b7": // Warehouse A: the sync did not fit, and the stick is nearly full.
		st.NowPlaying = &manifest.NowPlaying{
			Playlist: "safety-loop", Index: 2, Item: "safety-notice-2.png", Kind: "image",
			Since: now.Add(-14 * time.Second),
		}
		st.TempC = 52.1
		st.MediaTotalBytes = 15 << 30
		st.MediaFreeBytes = 1_181_116_006
		st.LastSyncResult = "error"
		st.SyncError = "needs 3.4 GB, has 1.1 GB"
		st.Tier = "low"
		st.Codecs = codecsFor("low")
		hb.SyncError = "needs 3.4 GB, has 1.1 GB"
	case "px-9a02f451": // Meeting room 3: a web page is on the screen.
		st.NowPlaying = &manifest.NowPlaying{
			Playlist: "room-signs", Index: 0, Item: "https://dashboards.example.com/rooms", Kind: "url",
			Since: now.Add(-2 * time.Minute),
		}
	case "px-2f81c604": // Front window: a video plays.
		st.NowPlaying = &manifest.NowPlaying{
			Playlist: "retail-promo", Index: 1, Item: "promo-fall.mp4", Kind: "video",
			Since: now.Add(-6 * time.Second),
		}
	}
	hb.Status = st
	return hb
}

// codecsFor gives a report of the kind that the player sends. A low-power box
// decodes H.264 only, which is what makes the item warnings of the playlist
// editor appear.
func codecsFor(tier string) manifest.CodecReport {
	yes := manifest.CodecSupport{Supported: true, Smooth: true, PowerEfficient: boolPtr(true)}
	soft := manifest.CodecSupport{Supported: true, Smooth: false, PowerEfficient: boolPtr(false)}
	no := manifest.CodecSupport{Supported: false}
	if tier == "low" {
		return manifest.CodecReport{
			"h264": {"1080": yes, "2160": no},
			"hevc": {"1080": no, "2160": no},
			"vp9":  {"1080": soft, "2160": no},
			"av1":  {"1080": no, "2160": no},
		}
	}
	return manifest.CodecReport{
		"h264": {"1080": yes, "2160": yes},
		"hevc": {"1080": yes, "2160": soft},
		"vp9":  {"1080": yes, "2160": soft},
		"av1":  {"1080": soft, "2160": no},
	}
}

func boolPtr(v bool) *bool { return &v }

// slug makes a host name out of a screen name.
func slug(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+32)
		default:
			if len(out) > 0 && out[len(out)-1] != '-' {
				out = append(out, '-')
			}
		}
	}
	return string(out)
}

// poll keeps the live screens checking in. It fetches the manifest, sends a
// heartbeat and acknowledges every command that the manifest carried, so the
// command list of the screen detail page walks from queued to delivered to
// acknowledged while a person watches it.
func poll(base, statePath string) error {
	st, err := loadState(statePath)
	if err != nil {
		return err
	}
	c, err := newClient(base)
	if err != nil {
		return err
	}
	// Only the screens that must look alive. The quiet one, the offline one and
	// the one that waits for approval stay silent on purpose.
	live := []screen{screens[0], screens[3], screens[4]}

	fmt.Println("the fake screens check in every 10 seconds; stop this with Ctrl-C")
	for {
		for _, s := range live {
			token := st.Tokens[s.id]
			if token == "" {
				continue
			}
			var m manifest.Manifest
			if err := c.bearer("GET", "/api/v1/manifest", token, nil, &m); err != nil {
				fmt.Printf("%s: %v\n", s.id, err)
				continue
			}
			hb := heartbeatFor(s)
			for _, cmd := range m.Commands {
				hb.Acks = append(hb.Acks, cmd.ID)
				fmt.Printf("%s ran %s\n", s.name, cmd.Type)
			}
			// The poll loop reports no hardware ID. A report with the first ID
			// would undo the clone conflict and the hardware swap that the UI is
			// there to show.
			hb.HardwareID = ""
			if err := c.bearer("POST", "/api/v1/heartbeat", token, hb, nil); err != nil {
				fmt.Printf("%s: %v\n", s.id, err)
			}
		}
		time.Sleep(10 * time.Second)
	}
}
