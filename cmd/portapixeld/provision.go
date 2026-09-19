package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/identity"
	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/playlist"
)

// DefaultPlaylistName is the directory of the default content on the media
// partition (D37).
const DefaultPlaylistName = "default"

// DefaultMediaDir is where the image keeps the default content, under the release
// root.
const DefaultMediaDir = "default-media"

// provisionCommand does the first boot steps that own TOML: the device ID, the
// default portapixel.toml and the default playlist (plan section 14, steps 2, 4
// and 5).
//
// The shell script does the partition work and calls this. Every step looks before
// it writes, so a power cut in the middle is safe and a second run changes
// nothing.
func provisionCommand(args []string) int {
	fs := flag.NewFlagSet("provision", flag.ExitOnError)
	p := addPathFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if err := os.MkdirAll(p.state, 0o755); err != nil {
		return fail("make %s: %v", p.state, err)
	}
	if err := os.MkdirAll(p.media, 0o755); err != nil {
		return fail("make %s: %v", p.media, err)
	}
	log := opslog.New(filepath.Join(p.state, opsLogName))

	// Step 2: the identity. Resolve writes state.json and takes a new hardware ID
	// as a repair (D21).
	id := identity.Derive("/")
	if _, err := identity.Resolve(p.state, id, log); err != nil {
		log.Log("provision.state.fail", err.Error())
	}
	fmt.Printf("device id %s (from the %s)\n", id.DeviceID, id.Source)

	// Step 4: the configuration file, with the ID in it.
	configPath := config.MediaPath(p.media)
	if _, err := os.Stat(configPath); err != nil {
		cfg := config.Default()
		cfg.Device.ID = id.DeviceID
		if err := fsutil.WriteFileAtomic(configPath, config.Render(cfg), 0o644); err != nil {
			return fail("write %s: %v", configPath, err)
		}
		log.Log("provision.config", "the default portapixel.toml is written")
		fmt.Printf("wrote %s\n", configPath)
	} else {
		// The file is the user's. Only the reference copy of the ID is ours, and a
		// wrong one confuses a person who reads the card (D21).
		if err := refreshConfigID(p.media, p.state, id.DeviceID); err != nil {
			log.Log("provision.config.id.fail", err.Error())
		}
		fmt.Printf("%s is there already\n", configPath)
	}

	// Step 5: the default content, when the media partition holds no playlist.
	installed, err := installDefaultMedia(p, id, log)
	if err != nil {
		return fail("%v", err)
	}
	if installed {
		fmt.Printf("installed the default playlist in %s\n", filepath.Join(p.media, DefaultPlaylistName))
	}
	log.Log("provision.done", "device="+id.DeviceID)
	return 0
}

// refreshConfigID writes the derived ID into the configuration file when the file
// holds another one. It changes nothing else.
func refreshConfigID(mediaRoot, stateDir, deviceID string) error {
	result := config.Load(mediaRoot, stateDir)
	if result.Config.Device.ID == deviceID {
		return nil
	}
	cfg := result.Config
	cfg.Device.ID = deviceID
	if errs := cfg.Validate(); len(errs) > 0 {
		return fmt.Errorf("the configuration is not correct, so the device id is not written: %w", errs)
	}
	return config.Save(mediaRoot, stateDir, cfg)
}

// installDefaultMedia copies the default content onto the media partition and
// writes its playlist.toml (D37). It does nothing when the partition already holds
// a playlist: the content of the user is never touched.
func installDefaultMedia(p *paths, id identity.Identity, log *opslog.Log) (bool, error) {
	if hasPlaylist(p.media) {
		return false, nil
	}
	source := filepath.Join(p.releases, DefaultMediaDir)
	files, err := mediaFiles(source)
	if err != nil || len(files) == 0 {
		// An image with no default content is not a fault. The player shows the
		// fallback screen, which is what a new device must show anyway (D18).
		log.Log("provision.media.none", "there is no default content in "+source)
		return false, nil
	}

	target := filepath.Join(p.media, DefaultPlaylistName)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return false, fmt.Errorf("make %s: %w", target, err)
	}
	items := make([]playlist.Item, 0, len(files))
	for _, name := range files {
		to := filepath.Join(target, name)
		if _, err := os.Stat(to); err != nil {
			if err := fsutil.CopyFileSync(filepath.Join(source, name), to); err != nil {
				return false, err
			}
		}
		// No duration: the image items take [playback].image_duration.
		items = append(items, playlist.Item{File: name})
	}

	file := filepath.Join(target, playlist.FileName)
	if _, err := os.Stat(file); err != nil {
		p := playlist.Playlist{Meta: playlist.Meta{Name: "Default"}, Items: items}
		if err := fsutil.WriteFileAtomic(file, playlist.Render(p), 0o644); err != nil {
			return false, fmt.Errorf("write %s: %w", file, err)
		}
	}
	log.Log("provision.media", fmt.Sprintf("%d files in %s", len(items), target))
	return true, nil
}

// hasPlaylist reports if the media root already holds a playlist directory. A
// name that starts with an underscore is never a playlist (ARCHITECTURE section
// 3).
func hasPlaylist(mediaRoot string) bool {
	entries, err := os.ReadDir(mediaRoot)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		if _, err := os.Stat(filepath.Join(mediaRoot, e.Name(), playlist.FileName)); err == nil {
			return true
		}
	}
	return false
}

// mediaFiles gives the names of the files in dir that the player can show, in
// name order. The order of the default playlist must be the same on every device.
func mediaFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch playlist.Kind(playlist.Item{File: e.Name()}) {
		case playlist.KindImage, playlist.KindVideo:
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}
