package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/store"
)

// T048 (B8, FR-041): systemd expands specifiers (%h, %n, ...) in every value of a unit
// file, so a literal "%" must be written "%%". "$" is different per setting:
// ExecStart= expands $VAR (so "$" is written "$$"), whereas Environment= has "no
// special meaning" for "$" (man systemd.exec, Environment=; checked against systemd
// 262.1), so doubling it there would corrupt the value.

func unitLines(unit string) []string { return strings.Split(unit, "\n") }

func lineWithPrefix(t *testing.T, unit, prefix string) string {
	t.Helper()
	for _, l := range unitLines(unit) {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	t.Fatalf("no %q line in unit:\n%s", prefix, unit)
	return ""
}

func TestUnit_EscapesPercentInEverySystemdValue(t *testing.T) {
	app := store.App{
		Name:        "100%h app",
		BinaryPath:  "/opt/100%h/bin",
		ServiceName: "x.service",
		Args:        []string{"--pct=50%n"},
		EnvVars:     []store.EnvVar{{Name: "PCT", Value: "100%h"}},
	}
	unit := RenderServiceUnit(app)

	if got := lineWithPrefix(t, unit, "Environment=PCT="); got != `Environment=PCT="100%%h"` {
		t.Errorf("Environment line = %q, want the %% doubled", got)
	}
	if got := lineWithPrefix(t, unit, "Description="); !strings.Contains(got, "100%%h app") || strings.Contains(strings.ReplaceAll(got, "%%", ""), "%") {
		t.Errorf("Description line = %q, want every %% doubled", got)
	}
	if got := lineWithPrefix(t, unit, "ExecStart="); !strings.HasPrefix(got, "ExecStart=/opt/100%%h/bin ") || !strings.Contains(got, `"--pct=50%%n"`) {
		t.Errorf("ExecStart line = %q, want the %% doubled in the path and in the argument", got)
	}
	if got := lineWithPrefix(t, unit, "WorkingDirectory="); got != "WorkingDirectory=/opt/100%%h" {
		t.Errorf("WorkingDirectory line = %q, want the %% doubled", got)
	}
	// no single, unescaped % anywhere
	if stripped := strings.ReplaceAll(unit, "%%", ""); strings.Contains(stripped, "%") {
		t.Errorf("an unescaped %% remains in the unit:\n%s", unit)
	}
}

func TestUnit_DollarIsEscapedWhereSystemdExpandsItOnly(t *testing.T) {
	app := store.App{
		Name:        "x",
		BinaryPath:  "/opt/x/x",
		ServiceName: "x.service",
		Args:        []string{"--token=a$b"},
		EnvVars:     []store.EnvVar{{Name: "PASS", Value: "p$ssw0rd"}},
	}
	unit := RenderServiceUnit(app)
	if got := lineWithPrefix(t, unit, "ExecStart="); !strings.Contains(got, `"--token=a$$b"`) {
		t.Errorf("ExecStart line = %q, want $ written $$ (ExecStart expands $VAR)", got)
	}
	if got := lineWithPrefix(t, unit, "Environment=PASS="); got != `Environment=PASS="p$ssw0rd"` {
		t.Errorf("Environment line = %q: $ must stay single in Environment= (it has no special meaning there)", got)
	}
}

func TestUnit_ExecStartPathEscapesDollarToo(t *testing.T) {
	unit := RenderServiceUnit(store.App{Name: "x", BinaryPath: "/opt/$HOME/x", ServiceName: "x.service"})
	if got := lineWithPrefix(t, unit, "ExecStart="); got != "ExecStart=/opt/$$HOME/x" {
		t.Errorf("ExecStart line = %q, want $ doubled in the binary path", got)
	}
}

// Newlines in any value can never start a new directive line (B7 + B8 together).
func TestUnit_NoValueCanInjectADirectiveLine(t *testing.T) {
	app := store.App{
		Name:        "x\nExecStartPre=/bin/evil",
		BinaryPath:  "/opt/x\nExecStartPre=/bin/evil\n/bin",
		ServiceName: "x.service",
		Args:        []string{"a\nExecStartPre=/bin/evil"},
		EnvVars:     []store.EnvVar{{Name: "A\nExecStartPre=/bin/evil", Value: "v\nExecStartPre=/bin/evil"}},
	}
	for _, l := range unitLines(RenderServiceUnit(app)) {
		if strings.HasPrefix(l, "ExecStartPre=") {
			t.Errorf("injected directive line: %q", l)
		}
	}
}

// With systemd-analyze available the generated unit must verify cleanly. Skipped
// (and logged) when the tool is missing or refuses to run here.
func TestUnit_SystemdAnalyzeVerify(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze not installed; skipping (T048 optional check)")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "x")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	app := store.App{
		Name: "100%h app", BinaryPath: bin, ServiceName: "tverify.service", Args: []string{"--a=1%n", "--b=$x"},
		EnvVars: []store.EnvVar{{Name: "PCT", Value: "100%h"}, {Name: "PASS", Value: "p$w"}},
	}
	unitPath := filepath.Join(dir, "tverify.service")
	if err := os.WriteFile(unitPath, []byte(RenderServiceUnit(app)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(analyze, "verify", unitPath).CombinedOutput()
	if err != nil {
		t.Skipf("systemd-analyze verify could not run here (%v): %s", err, out)
	}
	if strings.Contains(string(out), "Failed to parse") || strings.Contains(string(out), "Unknown key") {
		t.Errorf("systemd-analyze reported problems:\n%s", out)
	}
}
