package config

import (
	"testing"
	"time"
)

// T089 CR-9: at the server start, an invalid SERVICE_WATCH_INTERVAL is logged and replaced
// by the default (5 s) instead of failing the start (a typo in the env of a self-deploy must
// not stop the new binary); a value outside 2 s..60 s is still clamped.
func TestT089_CR9_InvalidWatchIntervalFallsBackAtStart(t *testing.T) {
	t.Setenv("QUEUE_DB_PATH", t.TempDir()+"/q.db")
	for _, c := range []struct {
		in   string
		want time.Duration
	}{
		{"5 sec", 5 * time.Second}, {"soon", 5 * time.Second}, {"", 5 * time.Second},
		{"1s", 2 * time.Second}, {"5m", 60 * time.Second}, {"10s", 10 * time.Second},
	} {
		t.Setenv("SERVICE_WATCH_INTERVAL", c.in)
		cfg, err := LoadServiceConfigForStart()
		if err != nil {
			t.Errorf("CR-9: SERVICE_WATCH_INTERVAL=%q failed the start: %v", c.in, err)
			continue
		}
		if cfg.ServiceWatchInterval != c.want {
			t.Errorf("CR-9: SERVICE_WATCH_INTERVAL=%q -> %v, want %v", c.in, cfg.ServiceWatchInterval, c.want)
		}
	}
	// Other settings keep failing the start: only the watch interval is lenient.
	t.Setenv("SERVICE_WATCH_INTERVAL", "")
	t.Setenv("COOKIE_SECURE", "perhaps")
	if _, err := LoadServiceConfigForStart(); err == nil {
		t.Fatal("COOKIE_SECURE=perhaps was accepted")
	}
}
