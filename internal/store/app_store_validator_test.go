package store

import (
	"errors"
	"testing"
)

// T047 (FR-040): the store-level backstop refuses invalid fields even when a caller
// skipped the API validation, and does nothing when no validator is installed.
func TestAppStore_InputValidatorBackstop(t *testing.T) {
	s := newTestAppStore(t)

	// no validator: anything goes (existing behaviour, used by tests and the legacy bootstrap)
	if _, err := s.Create("free", "s0", "/bin/x", "x.service", "o/x", "x"); err != nil {
		t.Fatalf("Create without validator: %v", err)
	}

	var seen []string
	s.SetInputValidator(func(binaryPath, serviceName, githubRepo, artifactName string) error {
		seen = append(seen, binaryPath)
		if binaryPath == "/bad" {
			return errors.New("binaryPath: nope")
		}
		return nil
	})

	if _, err := s.Create("bad", "s1", "/bad", "b.service", "o/b", "b"); !errors.Is(err, ErrInvalidAppInput) {
		t.Fatalf("Create with invalid input = %v, want ErrInvalidAppInput", err)
	}
	apps, _ := s.List()
	if len(apps) != 1 {
		t.Errorf("a refused Create must insert nothing, have %d apps", len(apps))
	}

	good, err := s.Create("good", "s2", "/opt/g/g", "g.service", "o/g", "g")
	if err != nil {
		t.Fatalf("Create valid: %v", err)
	}
	if err := s.Update(good.ID, "good", "/bad", "g.service", "o/g", "g", true); !errors.Is(err, ErrInvalidAppInput) {
		t.Fatalf("Update to invalid input = %v, want ErrInvalidAppInput", err)
	}
	after, _ := s.Get(good.ID)
	if after.BinaryPath != "/opt/g/g" {
		t.Errorf("a refused Update must change nothing, binaryPath = %q", after.BinaryPath)
	}
	if err := s.UpdateWithOptionalSecret(good.ID, "good", "newsecret", "/bad", "g.service", "o/g", "g", true); !errors.Is(err, ErrInvalidAppInput) {
		t.Fatalf("UpdateWithOptionalSecret to invalid input = %v, want ErrInvalidAppInput", err)
	}
	if len(seen) < 3 {
		t.Errorf("validator consulted %d times, want on every Create/Update", len(seen))
	}
}
