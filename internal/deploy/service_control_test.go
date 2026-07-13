package deploy

import (
	"errors"
	"strings"
	"testing"
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
