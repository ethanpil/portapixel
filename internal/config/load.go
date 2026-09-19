package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ethanpil/portapixel/internal/fsutil"
)

// Result is the answer of Load. Load always gives a configuration that the
// daemon can use, because a device with a damaged media partition must still
// come up on the network (D38).
type Result struct {
	Config Config
	// FromShadow is true when the configuration comes from the last-known-good
	// copy in the state directory. The UI shows a loud warning then.
	FromShadow bool
	// FromDefault is true when no file was readable and Load gave the defaults.
	FromDefault bool
	// Warning says what is wrong with the copy on PPMEDIA. It is empty when that
	// copy was good.
	Warning string
}

// MediaPath gives the path of the configuration file on PPMEDIA.
func MediaPath(mediaRoot string) string { return filepath.Join(mediaRoot, FileName) }

// ShadowPath gives the path of the last-known-good copy in the state directory.
func ShadowPath(stateDir string) string { return filepath.Join(stateDir, ShadowName) }

// Load reads the configuration (D38).
//
// It reads the copy on PPMEDIA first. After a good parse it mirrors that file to
// the state directory on ext4. If the copy on PPMEDIA is missing or bad, it
// falls back to the mirror and says why in Warning. If neither file is
// readable, it gives the defaults.
func Load(mediaRoot, stateDir string) Result {
	data, err := os.ReadFile(MediaPath(mediaRoot))
	if err == nil {
		cfg, parseErr := Parse(data)
		if parseErr == nil {
			mirror(stateDir, data)
			return Result{Config: cfg}
		}
		err = parseErr
	}
	warning := err.Error()

	shadow, shadowErr := os.ReadFile(ShadowPath(stateDir))
	if shadowErr == nil {
		cfg, parseErr := Parse(shadow)
		if parseErr == nil {
			return Result{Config: cfg, FromShadow: true, Warning: warning}
		}
		warning += fmt.Sprintf("; the shadow copy is also bad: %v", parseErr)
	}
	return Result{Config: Default(), FromDefault: true, Warning: warning}
}

// Save checks cfg, writes it to PPMEDIA, and mirrors it. The write is staged and
// committed with a rename, and it ends with an fsync (D41).
func Save(mediaRoot, stateDir string, cfg Config) error {
	if errs := cfg.Validate(); len(errs) > 0 {
		return errs
	}
	data := Render(cfg)
	if err := fsutil.WriteFileAtomic(MediaPath(mediaRoot), data, 0o644); err != nil {
		return err
	}
	mirror(stateDir, data)
	return nil
}

// mirror keeps the last-known-good copy in the state directory. It writes only
// when the content is different, because each write costs flash life and the
// daemon parses the file at each boot and after each hand edit.
func mirror(stateDir string, data []byte) {
	path := ShadowPath(stateDir)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return
	}
	fsutil.WriteFileAtomic(path, data, 0o644)
}
