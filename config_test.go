package main

import (
	"os"
	"testing"
)

func TestLoadServiceConfigDefaults(t *testing.T) {
	os.Unsetenv("LISTEN_ADDR")
	os.Unsetenv("DEPLOY_QUEUE_DB_PATH")
	os.Unsetenv("DEPLOY_QUEUE_MAX")
	os.Unsetenv("ADMIN_USERNAME")
	t.Setenv("ADMIN_PASSWORD", "secret")

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
	if cfg.AdminUsername != "admin" {
		t.Fatalf("expected default admin username, got %q", cfg.AdminUsername)
	}
	if cfg.AdminPassword != "secret" {
		t.Fatalf("expected admin password to be loaded, got %q", cfg.AdminPassword)
	}
}

func TestLoadServiceConfigWithAdminAuth(t *testing.T) {
	t.Setenv("ADMIN_USERNAME", "root")
	t.Setenv("ADMIN_PASSWORD", "hunter2")

	cfg, err := LoadServiceConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AdminUsername != "root" {
		t.Fatalf("expected admin username root, got %q", cfg.AdminUsername)
	}
	if cfg.AdminPassword != "hunter2" {
		t.Fatalf("expected admin password hunter2, got %q", cfg.AdminPassword)
	}
}

func TestLoadServiceConfigRejectsMissingAdminUsername(t *testing.T) {
	t.Setenv("ADMIN_USERNAME", "")
	t.Setenv("ADMIN_PASSWORD", "secret")

	_, err := LoadServiceConfig()
	if err == nil {
		t.Fatal("expected error for empty ADMIN_USERNAME")
	}
}

func TestLoadServiceConfigRejectsMissingAdminPassword(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "")

	_, err := LoadServiceConfig()
	if err == nil {
		t.Fatal("expected error for empty ADMIN_PASSWORD")
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
