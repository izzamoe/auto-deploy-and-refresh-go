package deploy

import (
	"errors"
	"strings"
	"testing"
	"time"
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
		if gotName != action || len(gotArgs) != 1 || gotArgs[0] != "bot.service" {
			t.Errorf("systemctl called as %q %v, want %q [bot.service]", gotName, gotArgs, action)
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
