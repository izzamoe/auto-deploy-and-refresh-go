package deploy

import (
	"fmt"
	"strings"
)

// allowedServiceActions is the fixed allow-list of systemctl verbs the admin
// UI may invoke against a managed service. Anything else is rejected so the
// action can never be an arbitrary systemctl subcommand.
var allowedServiceActions = map[string]bool{
	"start":   true,
	"stop":    true,
	"restart": true,
}

// systemctlUnit runs "systemctl <verb> [flags...] -- <unit>": "--" ends option parsing,
// so a unit name can never be read as a systemctl option (B22, FR-047).
func systemctlUnit(verb, unit string, flags ...string) ([]byte, error) {
	args := append(append([]string{}, flags...), "--", unit)
	return runSystemctl(verb, args...)
}

// ControlService runs "systemctl <action> -- <serviceName>" for a whitelisted
// action (start, stop, restart) and returns the combined command output. The
// serviceName is passed as a distinct exec argument (never a shell string), so
// no quoting/injection is possible; it is only checked for emptiness.
func ControlService(serviceName, action string) (string, error) {
	if strings.TrimSpace(serviceName) == "" {
		return "", fmt.Errorf("service control: empty service name")
	}
	if !allowedServiceActions[action] {
		return "", fmt.Errorf("service control: unsupported action %q", action)
	}
	out, err := systemctlUnit(action, serviceName)
	if err != nil {
		return string(out), fmt.Errorf("service control: %s %s: %w: %s", action, serviceName, err, string(out))
	}
	return string(out), nil
}

// ServiceStatus returns the systemd active-state of serviceName (e.g. "active",
// "inactive", "failed", "activating"). It never returns an error: systemctl
// is-active exits non-zero for inactive/failed units, but its stdout still
// carries the state word, which is what callers want to display.
func ServiceStatus(serviceName string) string {
	if strings.TrimSpace(serviceName) == "" {
		return "unknown"
	}
	out, _ := systemctlUnit("is-active", serviceName)
	state := strings.TrimSpace(string(out))
	if state == "" {
		return "unknown"
	}
	return state
}
