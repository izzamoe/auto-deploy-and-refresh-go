package store

import (
	"strings"
	"testing"
)

func newTestAppArgsStore(t *testing.T) *AppArgsStore {
	t.Helper()
	db := newTestDB(t)
	s, err := NewAppArgsStore(db)
	if err != nil {
		t.Fatalf("NewAppArgsStore: %v", err)
	}
	return s
}

func TestAppArgsStoreGetEmptyWhenMissing(t *testing.T) {
	t.Parallel()
	s := newTestAppArgsStore(t)

	args, err := s.Get("app-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %v", args)
	}
}

func TestAppArgsStoreSetAndGet(t *testing.T) {
	t.Parallel()
	s := newTestAppArgsStore(t)

	want := []string{"--port", "8080", "--message", "hello world", ""}
	if err := s.Set("app-1", want); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := s.Get("app-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Get() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Get()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAppArgsStoreSetReplacesAndClears(t *testing.T) {
	t.Parallel()
	s := newTestAppArgsStore(t)

	if err := s.Set("app-1", []string{"--old"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set("app-1", []string{"--new", "1"}); err != nil {
		t.Fatalf("Set replace: %v", err)
	}
	got, err := s.Get("app-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 2 || got[0] != "--new" || got[1] != "1" {
		t.Fatalf("Get() = %v, want [--new 1]", got)
	}

	if err := s.Set("app-1", []string{}); err != nil {
		t.Fatalf("Set clear: %v", err)
	}
	got, err = s.Get("app-1")
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected args cleared, got %v", got)
	}
}

func TestAppArgsStoreIsolatedPerApp(t *testing.T) {
	t.Parallel()
	s := newTestAppArgsStore(t)

	if err := s.Set("app-1", []string{"--one"}); err != nil {
		t.Fatalf("Set app-1: %v", err)
	}
	if err := s.Set("app-2", []string{"--two"}); err != nil {
		t.Fatalf("Set app-2: %v", err)
	}

	got, err := s.Get("app-1")
	if err != nil {
		t.Fatalf("Get app-1: %v", err)
	}
	if len(got) != 1 || got[0] != "--one" {
		t.Fatalf("app-1 args = %v, want [--one]", got)
	}
}

func TestAppArgsStoreDelete(t *testing.T) {
	t.Parallel()
	s := newTestAppArgsStore(t)

	if err := s.Set("app-1", []string{"--port", "8080"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Delete("app-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := s.Get("app-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected args deleted, got %v", got)
	}
	// Deleting an app with no stored args is not an error.
	if err := s.Delete("app-missing"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
}

func TestAppArgsStoreSetRejectsInvalidArgs(t *testing.T) {
	t.Parallel()
	s := newTestAppArgsStore(t)

	if err := s.Set("app-1", []string{"--flag", "line1\nExecStartPost=/bin/rm"}); err == nil {
		t.Fatal("expected Set to reject an argument containing a newline")
	}
	// Nothing must have been written for the rejected batch.
	got, err := s.Get("app-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no args persisted after rejection, got %v", got)
	}

	tooMany := make([]string, maxServiceArgs+1)
	for i := range tooMany {
		tooMany[i] = "--flag"
	}
	if err := s.Set("app-1", tooMany); err == nil {
		t.Fatalf("expected Set to reject more than %d arguments", maxServiceArgs)
	}
}

func TestValidateServiceArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple flag", "--port", false},
		{"value", "8080", false},
		{"empty is a legal argv entry", "", false},
		{"contains spaces", "hello world", false},
		{"equals form", "--config=/etc/app.yaml", false},
		{"dollar sign", "p$ssw0rd", false},
		{"percent sign", "100%", false},
		{"quotes", `say "hi"`, false},
		{"unicode", "café", false},
		{"max length", strings.Repeat("a", maxServiceArgLen), false},
		{"newline", "a\nb", true},
		{"carriage return", "a\rb", true},
		{"tab", "a\tb", true},
		{"null byte", "a\x00b", true},
		{"delete char", "a\x7fb", true},
		{"too long", strings.Repeat("a", maxServiceArgLen+1), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateServiceArg(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateServiceArg(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
