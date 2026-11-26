package config

import (
	"time"
	"net"
	"os"
	"strings"
	"testing"
)

func TestLoadServiceConfigDefaults(t *testing.T) {
	os.Unsetenv("LISTEN_ADDR")
	os.Unsetenv("DEPLOY_QUEUE_DB_PATH")
	os.Unsetenv("DEPLOY_QUEUE_MAX")

	cfg, err := LoadServiceConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ListenAddr != ":9000" {
		t.Fatalf("expected default listen addr, got %q", cfg.ListenAddr)
	}
	if cfg.QueueDBPath != "deploy-queue.db" {
		t.Fatalf("expected default queue db path, got %q", cfg.QueueDBPath)
	}
	if cfg.QueueMax != 10 {
		t.Fatalf("expected default queue max, got %d", cfg.QueueMax)
	}
	if cfg.DownloadDNS != "1.1.1.1" {
		t.Fatalf("expected default download DNS, got %q", cfg.DownloadDNS)
	}
}

func TestLoadServiceConfig_SessionKeyPath(t *testing.T) {
	t.Setenv("DEPLOY_QUEUE_DB_PATH", "/var/lib/auto-deploy/queue.db")
	t.Setenv("SESSION_KEY_PATH", "")
	cfg, err := LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SessionKeyPath != "/var/lib/auto-deploy/session.key" {
		t.Errorf("default session key path = %q, want session.key next to the DB (Q-1)", cfg.SessionKeyPath)
	}

	t.Setenv("SESSION_KEY_PATH", "/etc/auto-deploy/key")
	cfg, err = LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SessionKeyPath != "/etc/auto-deploy/key" {
		t.Errorf("SESSION_KEY_PATH override = %q", cfg.SessionKeyPath)
	}
}

// B28 (FR-053): ADMIN_USERNAME was dead configuration (nothing read it after the
// account moved into the database) yet an empty value aborted start. The name keeps
// the task's wording; the CORRECT behaviour asserted here is that start no longer
// fails. TERBUKTI = this test fails on HEAD with "ADMIN_USERNAME is required".
func TestLoadServiceConfig_EmptyAdminUsernameFails(t *testing.T) {
	t.Setenv("ADMIN_USERNAME", "")
	if _, err := LoadServiceConfig(); err != nil {
		t.Fatalf("B28: an empty (dead) ADMIN_USERNAME must not fail start, got: %v", err)
	}
}

func TestLoadServiceConfig_CookieSecureDefaultsToTrue(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want bool
	}{
		{"", true}, {"true", true}, {"1", true}, {"TRUE", true},
		{"false", false}, {"0", false}, {" False ", false},
	} {
		t.Setenv("COOKIE_SECURE", tc.env)
		cfg, err := LoadServiceConfig()
		if err != nil {
			t.Fatalf("COOKIE_SECURE=%q: %v", tc.env, err)
		}
		if cfg.CookieSecure != tc.want {
			t.Errorf("COOKIE_SECURE=%q: CookieSecure = %v, want %v (FR-010, Q-21)", tc.env, cfg.CookieSecure, tc.want)
		}
	}
	// A typo must not silently turn a security default off (or on).
	t.Setenv("COOKIE_SECURE", "flase")
	if _, err := LoadServiceConfig(); err == nil || !strings.Contains(err.Error(), "COOKIE_SECURE") {
		t.Errorf("invalid COOKIE_SECURE must fail start naming the variable, got %v", err)
	}
}

func TestLoadServiceConfig_TrustedProxyCIDRs(t *testing.T) {
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	cfg, err := LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.TrustedProxyCIDRs) != 0 {
		t.Errorf("default must be empty (Q-5), got %v", cfg.TrustedProxyCIDRs)
	}

	t.Setenv("TRUSTED_PROXY_CIDRS", " 10.0.0.0/8 , 192.0.2.7,fd00::/8 ,, ")
	cfg, err = LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.TrustedProxyCIDRs) != 3 {
		t.Fatalf("parsed %d entries, want 3: %v", len(cfg.TrustedProxyCIDRs), cfg.TrustedProxyCIDRs)
	}
	if !cfg.TrustedProxyCIDRs[1].Contains(net.ParseIP("192.0.2.7")) || cfg.TrustedProxyCIDRs[1].Contains(net.ParseIP("192.0.2.8")) {
		t.Error("a bare IP must become a single-address network")
	}

	t.Setenv("TRUSTED_PROXY_CIDRS", "10.0.0.0/8,not-a-cidr")
	_, err = LoadServiceConfig()
	if err == nil || !strings.Contains(err.Error(), "TRUSTED_PROXY_CIDRS") {
		t.Errorf("bad entry must fail start naming the variable, got %v", err)
	}
}

func TestLoadServiceConfigWithDownloadDNSOverride(t *testing.T) {
	t.Setenv("DOWNLOAD_DNS", "8.8.8.8")

	cfg, err := LoadServiceConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DownloadDNS != "8.8.8.8" {
		t.Fatalf("expected DNS override, got %q", cfg.DownloadDNS)
	}
}

// FR-053 / NFR-004: the account lives in the database and its first password is
// random, so the old account variables carry no meaning. Whatever they hold (set,
// empty, blank) must neither fail start nor change the configuration.
func TestLoadServiceConfigIgnoresRetiredAccountVariables(t *testing.T) {
	base, err := LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"", "root", "  "} {
		t.Setenv("ADMIN_USERNAME", v)
		cfg, err := LoadServiceConfig()
		if err != nil {
			t.Fatalf("retired variable set to %q must not fail start: %v", v, err)
		}
		if cfg.ListenAddr != base.ListenAddr || cfg.CookieSecure != base.CookieSecure || cfg.SessionKeyPath != base.SessionKeyPath {
			t.Errorf("retired variable changed the configuration: %+v vs %+v", cfg, base)
		}
	}
}

func TestLoadLegacyBootstrapConfigAllPresent(t *testing.T) {
	os.Unsetenv("WEBHOOK_SECRET")
	os.Unsetenv("DEPLOY_BINARY_PATH")
	os.Unsetenv("DEPLOY_SERVICE_NAME")
	os.Unsetenv("GITHUB_REPO")
	os.Unsetenv("ARTIFACT_NAME")
	t.Setenv("WEBHOOK_SECRET", "secret")
	t.Setenv("DEPLOY_BINARY_PATH", "/tmp/app")
	t.Setenv("DEPLOY_SERVICE_NAME", "app.service")
	t.Setenv("GITHUB_REPO", "owner/repo")
	t.Setenv("ARTIFACT_NAME", "artifact")

	cfg := LoadLegacyBootstrapConfig()
	if cfg == nil {
		t.Fatal("expected config, got nil")
	}
	if cfg.Secret != "secret" || cfg.BinaryPath != "/tmp/app" || cfg.ServiceName != "app.service" || cfg.GithubRepo != "owner/repo" || cfg.ArtifactName != "artifact" {
		t.Fatalf("unexpected legacy config: %+v", cfg)
	}
}

func TestLoadLegacyBootstrapConfigAllAbsent(t *testing.T) {
	os.Unsetenv("WEBHOOK_SECRET")
	os.Unsetenv("DEPLOY_BINARY_PATH")
	os.Unsetenv("DEPLOY_SERVICE_NAME")
	os.Unsetenv("GITHUB_REPO")
	os.Unsetenv("ARTIFACT_NAME")
	cfg := LoadLegacyBootstrapConfig()
	if cfg != nil {
		t.Fatalf("expected nil config, got %+v", cfg)
	}
}

func TestParseQueueConfigValid(t *testing.T) {
	n, path, err := parseQueueConfig("10", "deploy-queue.db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 10 {
		t.Fatalf("expected 10, got %d", n)
	}
	if path != "deploy-queue.db" {
		t.Fatalf("expected deploy-queue.db, got %q", path)
	}
}

func TestParseQueueConfigInvalid(t *testing.T) {
	for _, tc := range []string{"abc", "0", "-1", ""} {
		if _, _, err := parseQueueConfig(tc, "deploy-queue.db"); err == nil {
			t.Fatalf("expected error for %q", tc)
		}
	}
}

// specs/002 T045 (Q-3=A): SERVICE_WATCH_INTERVAL, default 5 s, kept within 2 s..60 s.
func TestLoadServiceConfig_ServiceWatchInterval(t *testing.T) {
	t.Setenv("SERVICE_WATCH_INTERVAL", "")
	cfg, err := LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceWatchInterval != 5*time.Second {
		t.Fatalf("default interval = %v, want 5s", cfg.ServiceWatchInterval)
	}
	for _, c := range []struct {
		in   string
		want time.Duration
	}{
		{"10s", 10 * time.Second}, {"7", 7 * time.Second}, {"1s", 2 * time.Second}, {"0", 2 * time.Second},
		{"5m", 60 * time.Second}, {" 3s ", 3 * time.Second},
	} {
		got, err := ParseServiceWatchInterval(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseServiceWatchInterval(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	t.Setenv("SERVICE_WATCH_INTERVAL", "soon")
	if _, err := LoadServiceConfig(); err == nil {
		t.Fatal("SERVICE_WATCH_INTERVAL=soon was accepted")
	}
}
