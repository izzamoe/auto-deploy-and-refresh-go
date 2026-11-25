package deploy

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// RemoveServiceUnit deletes the unit file auto-deploy generated for serviceName
// (Q-10: only on explicit request when an app is deleted; the default is to leave it).
// It never touches a file that does not start with the generated-unit marker
// (IsRefuseOverwriteError reports that case) and an absent file is not an error
// (removed=false). The service is disabled first so no dangling WantedBy symlink
// stays behind, then systemd is told to reload; a running service is NOT stopped.
func RemoveServiceUnit(serviceName string) (removed bool, err error) {
	base, fileName, err := serviceUnitBaseAndFile(serviceName)
	if err != nil {
		return false, err
	}
	if err := ValidateServiceUnitName(base); err != nil { // defense in depth, like ApplyServiceUnit
		return false, err
	}
	target := filepath.Join(systemdUnitDir, fileName)
	existing, readErr := os.ReadFile(target)
	switch {
	case errors.Is(readErr, os.ErrNotExist):
		return false, nil
	case readErr != nil:
		return false, fmt.Errorf("service_unit: read unit file: %w", readErr)
	}
	firstLine, _, _ := bytes.Cut(existing, []byte("\n"))
	if string(firstLine) != generatedUnitMarker {
		return false, errRefuseOverwrite
	}
	if out, err := systemctlUnit("disable", base); err != nil {
		return false, fmt.Errorf("service_unit: disable: %w: %s", err, string(out))
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("service_unit: remove unit file: %w", err)
	}
	if out, err := runSystemctl("daemon-reload"); err != nil {
		return true, fmt.Errorf("service_unit: daemon-reload: %w: %s", err, string(out))
	}
	return true, nil
}
