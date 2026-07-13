package store

import (
	"testing"
)

func newTestAppEnvStore(t *testing.T) *AppEnvStore {
	t.Helper()
	db := newTestDB(t)
	s, err := NewAppEnvStore(db)
	if err != nil {
		t.Fatalf("NewAppEnvStore: %v", err)
	}
	return s
}

func TestAppEnvStoreGetEmptyWhenMissing(t *testing.T) {
	t.Parallel()
	s := newTestAppEnvStore(t)

	vars, err := s.Get("app-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(vars) != 0 {
		t.Errorf("expected no env vars, got %v", vars)
	}
}

func TestAppEnvStoreSetAndGet(t *testing.T) {
	t.Parallel()
	s := newTestAppEnvStore(t)

	want := []EnvVar{{Name: "BOT_TOKEN", Value: "123:abc"}, {Name: "LOG_LEVEL", Value: "info"}}
	if err := s.Set("app-1", want); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := s.Get("app-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Get() = %v, want %v", got, want)
	}
}

func TestAppEnvStoreSetReplaces(t *testing.T) {
	t.Parallel()
	s := newTestAppEnvStore(t)

	if err := s.Set("app-1", []EnvVar{{Name: "A", Value: "1"}}); err != nil {
		t.Fatalf("Set first: %v", err)
	}
	if err := s.Set("app-1", []EnvVar{{Name: "B", Value: "2"}}); err != nil {
		t.Fatalf("Set second: %v", err)
	}
	got, err := s.Get("app-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 1 || got[0].Name != "B" {
		t.Fatalf("Get() = %v, want single B (Set should replace, not append)", got)
	}
}

func TestAppEnvStoreSetRejectsInvalidName(t *testing.T) {
	t.Parallel()
	s := newTestAppEnvStore(t)

	for _, bad := range []string{"has space", "1STARTS_DIGIT", "has-dash", "has=eq", ""} {
		if err := s.Set("app-1", []EnvVar{{Name: bad, Value: "x"}}); err == nil {
			t.Errorf("Set with invalid name %q returned nil, want error", bad)
		}
	}
}

func TestAppEnvStoreDelete(t *testing.T) {
	t.Parallel()
	s := newTestAppEnvStore(t)

	if err := s.Set("app-1", []EnvVar{{Name: "A", Value: "1"}}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Delete("app-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := s.Get("app-1")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no env vars after delete, got %v", got)
	}
}
