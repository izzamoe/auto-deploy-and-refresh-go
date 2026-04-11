package main

import (
	"fmt"
	"os"
	"strconv"
)

type ServiceConfig struct {
	ListenAddr    string
	QueueDBPath   string
	QueueMax      int
	AdminUsername string
	AdminPassword string
}

type LegacyBootstrapConfig struct {
	Secret       string
	BinaryPath   string
	ServiceName  string
	GithubRepo   string
	ArtifactName string
}

func LoadServiceConfig() (*ServiceConfig, error) {
	queueMax, queueDBPath, err := parseQueueConfig(
		envOrDefault("DEPLOY_QUEUE_MAX", "10"),
		envOrDefault("DEPLOY_QUEUE_DB_PATH", "deploy-queue.db"),
	)
	if err != nil {
		return nil, err
	}

	username, ok := os.LookupEnv("ADMIN_USERNAME")
	if !ok {
		username = "admin"
	} else if username == "" {
		return nil, fmt.Errorf("ADMIN_USERNAME is required")
	}

	password, ok := os.LookupEnv("ADMIN_PASSWORD")
	if !ok || password == "" {
		return nil, fmt.Errorf("ADMIN_PASSWORD is required")
	}

	return &ServiceConfig{
		ListenAddr:    envOrDefault("LISTEN_ADDR", ":9000"),
		QueueDBPath:   queueDBPath,
		QueueMax:      queueMax,
		AdminUsername: username,
		AdminPassword: password,
	}, nil
}

func LoadLegacyBootstrapConfig() *LegacyBootstrapConfig {
	secret, secretOK := os.LookupEnv("WEBHOOK_SECRET")
	binaryPath, binaryOK := os.LookupEnv("DEPLOY_BINARY_PATH")
	serviceName, serviceOK := os.LookupEnv("DEPLOY_SERVICE_NAME")
	githubRepo, repoOK := os.LookupEnv("GITHUB_REPO")
	artifactName, artifactOK := os.LookupEnv("ARTIFACT_NAME")

	if (!secretOK || secret == "") && (!binaryOK || binaryPath == "") && (!serviceOK || serviceName == "") && (!repoOK || githubRepo == "") && (!artifactOK || artifactName == "") {
		return nil
	}

	return &LegacyBootstrapConfig{
		Secret:       secret,
		BinaryPath:   binaryPath,
		ServiceName:  serviceName,
		GithubRepo:   githubRepo,
		ArtifactName: artifactName,
	}
}

// parseQueueConfig parses queue-related env vars and returns (queueMax, dbPath, error).
// Extracted for testability — config loading returns errors instead of exiting.
func parseQueueConfig(maxStr, dbPath string) (int, string, error) {
	n, err := strconv.Atoi(maxStr)
	if err != nil || n < 1 {
		return 0, "", fmt.Errorf("DEPLOY_QUEUE_MAX must be a positive integer, got %q", maxStr)
	}
	return n, dbPath, nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
