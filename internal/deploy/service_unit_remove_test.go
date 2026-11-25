package deploy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// T046 (Q-10): removing an app may also remove the unit file auto-deploy generated for
// it, but never a hand-written one.

func TestRemoveServiceUnit(t *testing.T) {
	dir := t.TempDir()
	original := systemdUnitDir
	systemdUnitDir = dir
	t.Cleanup(func() { systemdUnitDir = original })
	var calls [][]string
	stubSystemctl(t, func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	})

	t.Run("generated unit is removed, disabled first and daemon reloaded", func(t *testing.T) {
		calls = nil
		path := filepath.Join(dir, "gen.service")
		if err := os.WriteFile(path, []byte(generatedUnitMarker+"\n[Service]\nExecStart=/x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		removed, err := RemoveServiceUnit("gen.service")
		if err != nil || !removed {
			t.Fatalf("RemoveServiceUnit = (%v, %v), want (true, nil)", removed, err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("unit file still present: %v", err)
		}
		want := [][]string{{"disable", "--", "gen"}, {"daemon-reload"}}
		if len(calls) != len(want) || calls[0][0] != "disable" || calls[0][1] != "--" || calls[0][2] != "gen" || calls[1][0] != "daemon-reload" {
			t.Errorf("systemctl calls = %v, want %v", calls, want)
		}
	})

	t.Run("hand-written unit is never removed", func(t *testing.T) {
		calls = nil
		path := filepath.Join(dir, "hand.service")
		if err := os.WriteFile(path, []byte("[Service]\nExecStart=/mine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		removed, err := RemoveServiceUnit("hand.service")
		if removed || !IsRefuseOverwriteError(err) {
			t.Fatalf("RemoveServiceUnit = (%v, %v), want (false, refuse error)", removed, err)
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("hand-written unit must stay: %v", statErr)
		}
		if len(calls) != 0 {
			t.Errorf("systemctl must not be invoked for a hand-written unit: %v", calls)
		}
	})

	t.Run("absent unit is not an error", func(t *testing.T) {
		removed, err := RemoveServiceUnit("never.service")
		if removed || err != nil {
			t.Fatalf("RemoveServiceUnit = (%v, %v), want (false, nil)", removed, err)
		}
	})

	t.Run("invalid names are rejected before any I/O", func(t *testing.T) {
		for _, bad := range []string{"", "../etc/passwd", "a b.service", "-x.service", "a/b.service"} {
			if removed, err := RemoveServiceUnit(bad); removed || err == nil {
				t.Errorf("RemoveServiceUnit(%q) = (%v, %v), want an error", bad, removed, err)
			}
		}
	})
}
