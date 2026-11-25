package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// `auto-deploy upgrade [vX.Y.Z]` (FR-046, FR-137, Q-22, Q-29):
//
//  1. resolve a release TAG: the argument, or the latest release from the GitHub API;
//  2. download SHA256SUMS and install.sh from THAT tag's release assets (never from a
//     moving branch);
//  3. verify install.sh against SHA256SUMS; any mismatch aborts before a single byte is
//     executed or installed;
//  4. run the verified install.sh for the pinned tag. It verifies the release archive
//     against the same, already verified SHA256SUMS before unpacking it.
const (
	defaultUpgradeRepo              = "izzamoe/auto-deploy-and-refresh-go"
	defaultGitHubAPIBase            = "https://api.github.com"
	defaultGitHubDownloadBase       = "https://github.com"
	maxUpgradeAssetBytes      int64 = 1 << 20
	upgradeHTTPTimeout              = 60 * time.Second
)

var upgradeTagPattern = regexp.MustCompile(`^v[0-9][0-9A-Za-z._+-]{0,62}$`)

type upgradeConfig struct {
	Repo         string
	APIBase      string
	DownloadBase string
	HTTP         *http.Client
	Stdout       io.Writer
	Stderr       io.Writer
	// RunScript executes the verified install script for tag; a seam for tests.
	RunScript func(scriptPath, tag, sumsPath string, stdout, stderr io.Writer) error
}

func upgradeConfigFromEnv() upgradeConfig {
	env := func(key, def string) string {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return strings.TrimRight(v, "/")
		}
		return def
	}
	return upgradeConfig{
		Repo:         env("AUTO_DEPLOY_REPO", defaultUpgradeRepo),
		APIBase:      env("AUTO_DEPLOY_GITHUB_API_BASE", defaultGitHubAPIBase),
		DownloadBase: env("AUTO_DEPLOY_GITHUB_DOWNLOAD_BASE", defaultGitHubDownloadBase),
		HTTP:         &http.Client{Timeout: upgradeHTTPTimeout},
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		RunScript:    runInstallScript,
	}
}

// runUpgrade upgrades the installed binary in place. args are the arguments after the
// "upgrade" subcommand (an optional version tag). It returns a process exit code.
func runUpgrade(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			printUpgradeUsage(os.Stdout)
			return 0
		}
	}
	if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "upgrade: too many arguments (want at most a version tag, e.g. v1.2.3)")
		printUpgradeUsage(os.Stderr)
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "upgrade: writing /opt/auto-deploy and restarting the service needs root; re-run with sudo if this fails.")
	}
	tag := ""
	if len(args) == 1 {
		tag = args[0]
	}
	if err := upgradeFlow(context.Background(), upgradeConfigFromEnv(), tag); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "upgrade failed: %v\n", err)
		return 1
	}
	return 0
}

// upgradeFlow resolves the tag, verifies install.sh and runs it. It returns before
// anything is installed when any step fails.
func upgradeFlow(ctx context.Context, cfg upgradeConfig, tagArg string) error {
	tag := tagArg
	if tag == "" {
		latest, err := latestReleaseTag(ctx, cfg)
		if err != nil {
			return err
		}
		tag = latest
	}
	if !upgradeTagPattern.MatchString(tag) {
		return fmt.Errorf("%q is not a release tag (want vX.Y.Z)", tag)
	}
	fmt.Fprintf(cfg.Stderr, "Upgrading auto-deploy to %s\n", tag)

	sums, err := fetchReleaseAsset(ctx, cfg, tag, "SHA256SUMS")
	if err != nil {
		return fmt.Errorf("%w (releases before the checksum files cannot be upgraded to with this command)", err)
	}
	script, err := fetchReleaseAsset(ctx, cfg, tag, "install.sh")
	if err != nil {
		return err
	}
	if err := verifyAgainstSums(sums, "install.sh", script); err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "auto-deploy-upgrade-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	scriptPath, sumsPath := dir+"/install.sh", dir+"/SHA256SUMS"
	if err := os.WriteFile(scriptPath, script, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(sumsPath, sums, 0o600); err != nil {
		return err
	}
	return cfg.RunScript(scriptPath, tag, sumsPath, cfg.Stdout, cfg.Stderr)
}

func latestReleaseTag(ctx context.Context, cfg upgradeConfig) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", cfg.APIBase, cfg.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := cfg.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolve the latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("resolve the latest release: status %d from %s", resp.StatusCode, url)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxUpgradeAssetBytes)).Decode(&rel); err != nil || rel.TagName == "" {
		return "", fmt.Errorf("resolve the latest release: no tag_name in the response from %s", url)
	}
	return rel.TagName, nil
}

func fetchReleaseAsset(ctx context.Context, cfg upgradeConfig, tag, name string) ([]byte, error) {
	url := fmt.Sprintf("%s/%s/releases/download/%s/%s", cfg.DownloadBase, cfg.Repo, tag, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := cfg.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s for %s: status %d", name, tag, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUpgradeAssetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	if int64(len(data)) > maxUpgradeAssetBytes {
		return nil, fmt.Errorf("download %s: larger than %d bytes", name, maxUpgradeAssetBytes)
	}
	return data, nil
}

// verifyAgainstSums checks data against the entry for name in a sha256sum-format file.
func verifyAgainstSums(sums []byte, name string, data []byte) error {
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			want = strings.ToLower(fields[0])
		}
	}
	if want == "" {
		return fmt.Errorf("SHA256SUMS has no entry for %s; refusing to run it", name)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("checksum mismatch for %s: SHA256SUMS says %s, the download is %s; nothing was installed", name, want, got)
	}
	return nil
}

// runInstallScript runs the verified script with the pinned tag. The already verified
// SHA256SUMS is handed over so the script checks the archive against the same file.
func runInstallScript(scriptPath, tag, sumsPath string, stdout, stderr io.Writer) error {
	cmd := exec.Command("sh", scriptPath, tag)
	cmd.Env = append(os.Environ(), "AUTO_DEPLOY_SHA256SUMS_FILE="+sumsPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func printUpgradeUsage(w *os.File) {
	fmt.Fprint(w, `Usage: auto-deploy upgrade [vX.Y.Z]

Upgrades the installed auto-deploy binary in place. Defaults to the latest stable
GitHub release; pass a version tag to pin a specific release.

The command downloads install.sh and SHA256SUMS from the release's own assets (never
from a branch), verifies install.sh against SHA256SUMS, and runs it for that tag; the
script verifies the release archive against the same file before installing it.

Preserves /etc/auto-deploy.env and the existing database; overwrites the binary and
systemd unit, then restarts the service. Requires root (use sudo).

Overrides: AUTO_DEPLOY_REPO, AUTO_DEPLOY_GITHUB_API_BASE, AUTO_DEPLOY_GITHUB_DOWNLOAD_BASE.
`)
}

var _ = runtime.GOARCH
