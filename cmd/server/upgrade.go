package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// defaultUpgradeScriptURL points at the install script, which doubles as the
// upgrade path: it fetches the latest release (or a pinned tag), overwrites the
// binary and systemd unit, preserves /etc/auto-deploy.env and the existing
// database, then restarts the service. Override with AUTO_DEPLOY_INSTALL_URL.
const defaultUpgradeScriptURL = "https://raw.githubusercontent.com/izzamoe/auto-deploy-and-refresh-go/master/install.sh"

// runUpgrade upgrades the installed binary in place by re-running the install
// script. args are the arguments after the "upgrade" subcommand (an optional
// version tag). It streams the script output and returns a process exit code.
func runUpgrade(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			printUpgradeUsage(os.Stdout)
			return 0
		}
	}

	scriptURL := os.Getenv("AUTO_DEPLOY_INSTALL_URL")
	if scriptURL == "" {
		scriptURL = defaultUpgradeScriptURL
	}

	pipeline, err := buildUpgradePipeline(scriptURL, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		printUpgradeUsage(os.Stderr)
		return 2
	}

	if _, err := exec.LookPath("curl"); err != nil {
		fmt.Fprintln(os.Stderr, "upgrade: curl is required but was not found in PATH")
		return 1
	}

	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "upgrade: writing /opt/auto-deploy and restarting the service needs root; re-run with sudo if this fails.")
	}

	fmt.Fprintf(os.Stderr, "Upgrading auto-deploy via %s\n", scriptURL)
	cmd := exec.Command("sh", "-c", pipeline)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "upgrade failed: %v\n", err)
		return 1
	}
	return 0
}

// buildUpgradePipeline builds the `curl ... | sh -s -- [version]` command that
// re-runs the install script. At most one positional argument (a version tag)
// is accepted; validation of the tag itself is delegated to install.sh.
func buildUpgradePipeline(scriptURL string, args []string) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("upgrade: too many arguments (want at most a version tag, e.g. v1.2.3)")
	}
	pipeline := fmt.Sprintf("curl -fsSL %s | sh -s --", shellQuote(scriptURL))
	if len(args) == 1 {
		pipeline += " " + shellQuote(args[0])
	}
	return pipeline, nil
}

func printUpgradeUsage(w *os.File) {
	fmt.Fprint(w, `Usage: auto-deploy upgrade [vX.Y.Z]

Upgrades the installed auto-deploy binary in place by re-running the install
script. Defaults to the latest stable GitHub release; pass a version tag to pin
a specific release.

Preserves /etc/auto-deploy.env and the existing database; overwrites the binary
and systemd unit, then restarts the service. Requires root (use sudo) and curl.

Override the script URL with AUTO_DEPLOY_INSTALL_URL.
`)
}

// shellQuote wraps s in single quotes for safe interpolation into an `sh -c`
// pipeline, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
