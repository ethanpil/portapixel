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
	// Repaired holds the faults that the file had. Load keeps every other value
	// that the person wrote and puts the default value in each of these fields.
	// It is empty when the file breaks no rule.
	Repaired []FieldError
	// Warning says what is wrong with the copy on PPMEDIA. It is empty when that
	// copy was good.
	Warning string
}

// MediaPath gives the path of the configuration file on PPMEDIA.
func MediaPath(mediaRoot string) string { return filepath.Join(mediaRoot, FileName) }

// ShadowPath gives the path of the last-known-good copy in the state directory.
func ShadowPath(stateDir string) string { return filepath.Join(stateDir, ShadowName) }

// Load reads the configuration (D38). It never writes to the media root.
//
// It reads the copy on PPMEDIA first. A file that the TOML parser cannot read is
// no file at all: Load then falls back to the copy in the state directory, and
// to the defaults when that copy is bad as well.
//
// A file that parses but breaks a rule is repaired field by field: every value
// that the person wrote stays, and each bad field takes its default value. One
// typed character must not throw away the WiFi key, the password and the
// schedule that stand beside it. Load mirrors the file to the state directory
// only when the file breaks no rule, so a repaired file never replaces the
// last-known-good copy.
func Load(mediaRoot, stateDir string) Result {
	data, err := os.ReadFile(MediaPath(mediaRoot))
	if err == nil {
		cfg, parseErr := Parse(data)
		if parseErr == nil {
			cfg, bad := Repair(cfg)
			if len(bad) == 0 {
				mirror(stateDir, data)
				return Result{Config: cfg}
			}
			return Result{Config: cfg, Repaired: bad, Warning: repairWarning(FileName, bad)}
		}
		err = parseErr
	}
	warning := err.Error()

	shadow, shadowErr := os.ReadFile(ShadowPath(stateDir))
	if shadowErr == nil {
		cfg, parseErr := Parse(shadow)
		if parseErr == nil {
			cfg, bad := Repair(cfg)
			out := Result{Config: cfg, FromShadow: true, Repaired: bad, Warning: warning}
			if len(bad) > 0 {
				out.Warning += "; " + repairWarning(ShadowName, bad)
			}
			return out
		}
		warning += fmt.Sprintf("; the shadow copy is also bad: %v", parseErr)
	}
	return Result{Config: Default(), FromDefault: true, Warning: warning}
}

// repairWarning says what Load changed. The admin UI shows it, so it names each
// field and says what the device does now.
func repairWarning(name string, bad Errors) string {
	value, them := "values", "each of them"
	if len(bad) == 1 {
		value, them = "value", "it"
	}
	msg := fmt.Sprintf("%s has %d bad %s (%s); the device uses the default for %s",
		name, len(bad), value, bad.Error(), them)
	if hasField(bad, "web.password") {
		msg += ". The admin password is the default password now: change it"
	}
	return msg
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
//
// The mode is 0600. The file holds the WiFi key, the fleet token and the admin
// password in clear, the state directory is on ext4, which keeps modes, and SSH
// is on by default: another local account must not be able to read the file.
func mirror(stateDir string, data []byte) {
	path := ShadowPath(stateDir)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return
	}
	fsutil.WriteFileAtomic(path, data, 0o600)
}
