package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type ServiceConfig struct {
	ListenAddr  string
	QueueDBPath string
	QueueMax    int
	DownloadDNS string
	// CookieSecure sets the Secure flag (and the __Host- name prefix) on the admin session cookie. Default true (FR-010); plain-HTTP installations must set COOKIE_SECURE=false explicitly.
	CookieSecure bool
	// GitHubToken authenticates GitHub Releases API calls. Optional; empty means unauthenticated requests (subject to GitHub's lower rate limit).
	GitHubToken string
	// SelfServiceName is the systemd unit name of auto-deploy itself, used by the
	// admin "Application Logs" view to read this service's own journal for
	// diagnosis. Override via SELF_SERVICE_NAME when the unit is named differently.
	SelfServiceName string
	// SessionKeyPath is where the persistent session signing key lives (Q-1).
	// Default: session.key next to the queue database. Override via SESSION_KEY_PATH.
	SessionKeyPath string
	// TrustedProxyCIDRs lists the proxy address ranges whose CF-Connecting-IP /
	// X-Forwarded-For / X-Forwarded-Proto headers are believed (Q-5). Default empty:
	// forwarding headers are ignored and the socket address is the client IP.
	TrustedProxyCIDRs []*net.IPNet
	// ServiceWatchInterval is the round interval of the shared systemd watcher of the
	// realtime service topic (specs/002 Q-3=A): SERVICE_WATCH_INTERVAL, a Go duration
	// ("5s") or whole seconds ("5"); default 5 s, kept within 2 s..60 s.
	ServiceWatchInterval time.Duration
}

// Bounds of SERVICE_WATCH_INTERVAL (data-model.md "ServiceWatcher").
const (
	DefaultServiceWatchInterval = 5 * time.Second
	MinServiceWatchInterval     = 2 * time.Second
	MaxServiceWatchInterval     = 60 * time.Second
)

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

	trusted, err := ParseCIDRList(os.Getenv("TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return nil, err
	}

	cookieSecure, err := parseBoolEnv("COOKIE_SECURE", true)
	if err != nil {
		return nil, err
	}

	watchInterval, err := ParseServiceWatchInterval(os.Getenv("SERVICE_WATCH_INTERVAL"))
	if err != nil {
		return nil, err
	}

	return &ServiceConfig{
		ListenAddr:        envOrDefault("LISTEN_ADDR", ":9000"),
		QueueDBPath:       queueDBPath,
		QueueMax:          queueMax,
		DownloadDNS:       envOrDefault("DOWNLOAD_DNS", "1.1.1.1"),
		CookieSecure:      cookieSecure,
		GitHubToken:       envOrDefault("GITHUB_TOKEN", ""),
		SelfServiceName:   envOrDefault("SELF_SERVICE_NAME", "auto-deploy.service"),
		SessionKeyPath:    envOrDefault("SESSION_KEY_PATH", filepath.Join(filepath.Dir(queueDBPath), "session.key")),
		TrustedProxyCIDRs: trusted,

		ServiceWatchInterval: watchInterval,
	}, nil
}

// ParseServiceWatchInterval reads SERVICE_WATCH_INTERVAL: empty is the default, a Go
// duration ("5s", "1m") or whole seconds ("5"); a value outside 2 s..60 s is clamped.
func ParseServiceWatchInterval(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultServiceWatchInterval, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		n, nerr := strconv.Atoi(v)
		if nerr != nil {
			return 0, fmt.Errorf("SERVICE_WATCH_INTERVAL must be a duration such as 5s or a number of seconds, got %q", v)
		}
		d = time.Duration(n) * time.Second
	}
	switch {
	case d < MinServiceWatchInterval:
		d = MinServiceWatchInterval
	case d > MaxServiceWatchInterval:
		d = MaxServiceWatchInterval
	}
	return d, nil
}

// ParseCIDRList parses a comma-separated list of CIDRs or bare IPs (the value of
// TRUSTED_PROXY_CIDRS). An empty string is an empty list; a bad entry is an error
// that names the variable.
func ParseCIDRList(list string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, entry := range strings.Split(list, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS: invalid entry %q (want a CIDR such as 10.0.0.0/8 or an IP address)", entry)
			}
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS: invalid entry %q (want a CIDR such as 10.0.0.0/8 or an IP address)", entry)
		}
		out = append(out, n)
	}
	return out, nil
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

// parseBoolEnv reads a boolean variable: unset or empty -> def; true/1/yes/on and
// false/0/no/off are accepted; anything else is a start error naming the variable
// (a typo must never silently weaken a security default).
func parseBoolEnv(key string, def bool) (bool, error) {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "":
		return def, nil
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%s must be true or false, got %q", key, os.Getenv(key))
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
