package deploy

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/progress"
)

func TestControlServiceRunsWhitelistedActions(t *testing.T) {
	var gotName string
	var gotArgs []string
	restore := SetRunSystemctlForTest(func(name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = args
		return []byte("ok"), nil
	})
	defer restore()

	for _, action := range []string{"start", "stop", "restart"} {
		gotName, gotArgs = "", nil
		out, err := ControlService("bot.service", action)
		if err != nil {
			t.Fatalf("ControlService(%q): %v", action, err)
		}
		if out != "ok" {
			t.Errorf("output = %q, want ok", out)
		}
		if gotName != action || len(gotArgs) != 2 || gotArgs[0] != "--" || gotArgs[1] != "bot.service" {
			t.Errorf("systemctl called as %q %v, want %q [-- bot.service]", gotName, gotArgs, action)
		}
	}
}

func TestControlServiceRejectsUnknownAction(t *testing.T) {
	called := false
	restore := SetRunSystemctlForTest(func(string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	defer restore()

	for _, bad := range []string{"status", "enable", "disable", "kill", "", "start; rm -rf /"} {
		if _, err := ControlService("bot.service", bad); err == nil {
			t.Errorf("ControlService with action %q returned nil, want error", bad)
		}
	}
	if called {
		t.Error("systemctl must not run for a rejected action")
	}
}

func TestControlServiceRejectsEmptyServiceName(t *testing.T) {
	if _, err := ControlService("", "start"); err == nil {
		t.Fatal("expected error for empty service name")
	}
}

func TestServiceStatusReturnsTrimmedState(t *testing.T) {
	restore := SetRunSystemctlForTest(func(name string, args ...string) ([]byte, error) {
		if name != "is-active" {
			t.Errorf("expected is-active, got %q", name)
		}
		return []byte("active\n"), nil
	})
	defer restore()

	if got := ServiceStatus("bot.service"); got != "active" {
		t.Errorf("ServiceStatus = %q, want active", got)
	}
}

func TestServiceStatusInactiveIsNotAnError(t *testing.T) {
	restore := SetRunSystemctlForTest(func(string, ...string) ([]byte, error) {
		// systemctl is-active exits non-zero for inactive units.
		return []byte("inactive\n"), errors.New("exit status 3")
	})
	defer restore()

	if got := ServiceStatus("bot.service"); got != "inactive" {
		t.Errorf("ServiceStatus = %q, want inactive", got)
	}
}

func TestCaptureServiceLogs(t *testing.T) {
	restore := SetRunJournalctlForTest(func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-u bot.service") {
			t.Errorf("journalctl args = %v, want -u bot.service", args)
		}
		return []byte("line1\nline2\n"), nil
	})
	defer restore()

	if got := CaptureServiceLogs("bot.service"); got != "line1\nline2\n" {
		t.Errorf("CaptureServiceLogs = %q", got)
	}
}

func TestCaptureServiceLogsEmptyServiceName(t *testing.T) {
	if got := CaptureServiceLogs("  "); got != "" {
		t.Errorf("CaptureServiceLogs(blank) = %q, want empty", got)
	}
}

func TestCaptureServiceLogsSincePassesSinceFlag(t *testing.T) {
	var gotArgs string
	restore := SetRunJournalctlForTest(func(args ...string) ([]byte, error) {
		gotArgs = strings.Join(args, " ")
		return []byte("deploy-window-logs\n"), nil
	})
	defer restore()

	since := time.Unix(1_700_000_000, 0)
	got := CaptureServiceLogsSince("bot.service", since)
	if got != "deploy-window-logs\n" {
		t.Errorf("CaptureServiceLogsSince = %q", got)
	}
	if !strings.Contains(gotArgs, "-u bot.service") {
		t.Errorf("journalctl args = %q, want -u bot.service", gotArgs)
	}
	if !strings.Contains(gotArgs, "--since @1700000000") {
		t.Errorf("journalctl args = %q, want --since @1700000000", gotArgs)
	}
	// Scoping to a start time must NOT also impose a line cap, or a chatty
	// deploy window would be silently truncated.
	if strings.Contains(gotArgs, "-n ") {
		t.Errorf("journalctl args = %q, want no -n limit", gotArgs)
	}
}

func TestCaptureServiceLogsSinceEmptyServiceName(t *testing.T) {
	if got := CaptureServiceLogsSince("  ", time.Unix(0, 0)); got != "" {
		t.Errorf("CaptureServiceLogsSince(blank) = %q, want empty", got)
	}
}

func TestCaptureServiceLogsLinesPassesCount(t *testing.T) {
	var gotArgs string
	restore := SetRunJournalctlForTest(func(args ...string) ([]byte, error) {
		gotArgs = strings.Join(args, " ")
		return []byte("out"), nil
	})
	defer restore()

	CaptureServiceLogsLines("bot.service", 50)
	if !strings.Contains(gotArgs, "-n 50") {
		t.Errorf("journalctl args = %q, want -n 50", gotArgs)
	}
}

func TestCaptureServiceLogsLinesUnlimitedOmitsCount(t *testing.T) {
	var gotArgs string
	restore := SetRunJournalctlForTest(func(args ...string) ([]byte, error) {
		gotArgs = strings.Join(args, " ")
		return []byte("out"), nil
	})
	defer restore()

	CaptureServiceLogsLines("bot.service", 0)
	if strings.Contains(gotArgs, "-n ") {
		t.Errorf("journalctl args = %q, want no -n limit for unlimited", gotArgs)
	}
	if !strings.Contains(gotArgs, "--no-tail") {
		t.Errorf("journalctl args = %q, want --no-tail for unlimited", gotArgs)
	}
}

// B22 / FR-047: a unit name is always passed after "--", so a name can never be read as
// a systemctl option.
func TestSystemctl_UnitNameIsAlwaysAfterDoubleDash(t *testing.T) {
	dir := t.TempDir()
	defer SetSystemdUnitDirForTest(dir)()
	var calls [][]string
	defer SetRunSystemctlForTest(func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if name == "show" {
			return []byte("0\n"), nil
		}
		return []byte("active\n"), nil
	})()

	app := testApp(t.TempDir())
	for _, action := range []string{"start", "stop", "restart"} {
		if _, err := ControlService(app.ServiceName, action); err != nil {
			t.Fatal(err)
		}
	}
	ServiceStatus(app.ServiceName)
	if err := ApplyServiceUnit(*app); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveServiceUnit(app.ServiceName); err != nil {
		t.Fatal(err)
	}
	readNRestarts(app.ServiceName)
	stubDeploySleep(t)
	if err := os.WriteFile(app.BinaryPath, elfBinary("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := DeployWithControl(app, "job-b22", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new")), nil); err != nil {
		t.Fatal(err)
	}

	unitVerbs := map[string]bool{"start": true, "stop": true, "restart": true, "is-active": true, "show": true, "enable": true, "disable": true}
	checked := 0
	for _, c := range calls {
		if !unitVerbs[c[0]] {
			continue
		}
		checked++
		if len(c) < 3 || c[len(c)-2] != "--" {
			t.Errorf("B22: systemctl %v: the unit name must come right after \"--\"", c[1:])
		}
	}
	if checked < 8 {
		t.Fatalf("only %d unit-addressed systemctl calls were observed; the test no longer exercises the code", checked)
	}
}

// B22 / FR-047 / Q-15: the unit runs as root without hardening by default (compatible
// with existing apps) and renders User= and the base hardening directives when asked.
func TestUnit_RunAsUserAndHardeningAreRendered(t *testing.T) {
	app := testApp(t.TempDir())
	plain := RenderServiceUnit(*app)
	for _, forbidden := range []string{"User=", "NoNewPrivileges", "ProtectSystem", "PrivateTmp"} {
		if strings.Contains(plain, forbidden) {
			t.Errorf("an app without runAsUser/hardening must keep the old unit (found %q)", forbidden)
		}
	}
	app.RunAsUser = "svc-app"
	app.Hardening = true
	hardened := RenderServiceUnit(*app)
	for _, want := range []string{"\nUser=svc-app\n", "\nNoNewPrivileges=yes\n", "\nProtectSystem=full\n", "\nPrivateTmp=yes\n"} {
		if !strings.Contains(hardened, want) {
			t.Errorf("B22: the unit lacks %q:\n%s", strings.TrimSpace(want), hardened)
		}
	}
	// the directives live in [Service]
	if i, j := strings.Index(hardened, "[Service]"), strings.Index(hardened, "NoNewPrivileges"); i < 0 || j < i {
		t.Error("hardening directives must be inside [Service]")
	}
	app.RunAsUser = ""
	rootHardened := RenderServiceUnit(*app)
	if strings.Contains(rootHardened, "User=") || !strings.Contains(rootHardened, "NoNewPrivileges=yes") {
		t.Errorf("hardening without runAsUser keeps root but applies the directives:\n%s", rootHardened)
	}
}

func TestValidateRunAsUser(t *testing.T) {
	for _, ok := range []string{"", "svc", "svc-app", "_svc", "app1", "a" + strings.Repeat("b", 30)} {
		if err := ValidateRunAsUser(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"Root", "1app", "-svc", "svc app", "svc\nUser=root", "svc;id", "svc%h", "a" + strings.Repeat("b", 40), "svc$x", "../x"} {
		if err := ValidateRunAsUser(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}
