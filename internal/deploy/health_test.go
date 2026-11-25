package deploy

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/progress"
)

// healthScript drives systemctl for the health-check tests: is-active answers come
// from activeSeq (the last one repeats), NRestarts from restartsSeq (likewise).
type healthScript struct {
	mu          sync.Mutex
	activeSeq   []string
	restartsSeq []string
	activeN     int
	showN       int
	restarts    int // "systemctl restart" calls
	slept       time.Duration
}

func (h *healthScript) run(name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch name {
	case "restart":
		h.restarts++
		return []byte("restarted"), nil
	case "is-active":
		i := h.activeN
		if i >= len(h.activeSeq) {
			i = len(h.activeSeq) - 1
		}
		h.activeN++
		st := h.activeSeq[i]
		if st != "active" {
			return []byte(st + "\n"), fmt.Errorf("exit status 3")
		}
		return []byte(st + "\n"), nil
	case "show":
		if len(h.restartsSeq) == 0 {
			return nil, fmt.Errorf("show unsupported")
		}
		i := h.showN
		if i >= len(h.restartsSeq) {
			i = len(h.restartsSeq) - 1
		}
		h.showN++
		return []byte(h.restartsSeq[i] + "\n"), nil
	}
	return nil, fmt.Errorf("unexpected systemctl command %q", name)
}

// stubHealth installs the script and a virtual clock for the health loop.
func stubHealth(t *testing.T, h *healthScript) {
	t.Helper()
	stubSystemctl(t, h.run)
	original := deployHealthCheckSleep
	deployHealthCheckSleep = func(d time.Duration) {
		h.mu.Lock()
		h.slept += d
		h.mu.Unlock()
	}
	t.Cleanup(func() { deployHealthCheckSleep = original })
}

// B17 / FR-042: one "active" followed by a crash is NOT a healthy deploy.
func TestHealth_ActiveOnceThenFailedIsNotSuccess(t *testing.T) {
	a := testApp(t.TempDir())
	original := elfBinary("old-binary")
	if err := os.WriteFile(a.BinaryPath, original, 0755); err != nil {
		t.Fatal(err)
	}
	h := &healthScript{activeSeq: []string{"active", "failed"}}
	stubHealth(t, h)

	_, err := DeployWithControl(a, "job-b17-a", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil)
	if err == nil {
		t.Fatal("B17: deploy reported success although the service was active only once and then failed")
	}
	assertFileBytes(t, a.BinaryPath, original) // rolled back
}

// B17 / FR-042: a service that restarts in a loop is "active" at every sample, but
// NRestarts climbs -> not healthy.
func TestHealth_NRestartsIncreaseFails(t *testing.T) {
	a := testApp(t.TempDir())
	original := elfBinary("old-binary")
	if err := os.WriteFile(a.BinaryPath, original, 0755); err != nil {
		t.Fatal(err)
	}
	h := &healthScript{activeSeq: []string{"active"}, restartsSeq: []string{"0", "0", "1", "2", "3"}}
	stubHealth(t, h)

	_, err := DeployWithControl(a, "job-b17-b", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil)
	if err == nil {
		t.Fatal("B17: deploy reported success although NRestarts kept increasing")
	}
	if !strings.Contains(err.Error(), "restart") {
		t.Errorf("error %q should explain the restart loop", err.Error())
	}
	assertFileBytes(t, a.BinaryPath, original)
}

// Control (Q-13): 3 consecutive active samples with a constant NRestarts succeed,
// without waiting the whole 30 s window.
func TestHealth_StableServiceSucceedsAfterThreeActiveSamples(t *testing.T) {
	a := testApp(t.TempDir())
	if err := os.WriteFile(a.BinaryPath, elfBinary("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	h := &healthScript{activeSeq: []string{"active"}, restartsSeq: []string{"0"}}
	stubHealth(t, h)

	if _, err := DeployWithControl(a, "job-b17-c", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil); err != nil {
		t.Fatalf("a stable service must pass: %v", err)
	}
	if h.activeN < 3 {
		t.Errorf("is-active sampled %d times, want >= 3 (FR-042)", h.activeN)
	}
	if h.slept > 30*time.Second {
		t.Errorf("health check waited %v, want <= 30 s", h.slept)
	}
}

// FR-042: with a healthUrl, a 2xx answer after the unit is stable completes the deploy;
// the URL is retried until 30 s have passed.
func TestHealth_HealthURLBecomesReady(t *testing.T) {
	a := testApp(t.TempDir())
	if err := os.WriteFile(a.BinaryPath, elfBinary("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	a.HealthURL = "http://127.0.0.1:9/healthz"
	h := &healthScript{activeSeq: []string{"active"}, restartsSeq: []string{"0"}}
	stubHealth(t, h)
	calls := 0
	orig := healthHTTPGet
	healthHTTPGet = func(string) (int, error) {
		calls++
		if calls < 3 {
			return 503, nil
		}
		return 200, nil
	}
	t.Cleanup(func() { healthHTTPGet = orig })

	if _, err := DeployWithControl(a, "job-b17-url", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil); err != nil {
		t.Fatalf("deploy with a URL that becomes ready must succeed: %v", err)
	}
	if calls != 3 {
		t.Errorf("health URL polled %d times, want 3", calls)
	}
}

func TestHealth_HealthURLNeverReadyRollsBack(t *testing.T) {
	a := testApp(t.TempDir())
	original := elfBinary("old-binary")
	if err := os.WriteFile(a.BinaryPath, original, 0755); err != nil {
		t.Fatal(err)
	}
	a.HealthURL = "http://127.0.0.1:9/healthz"
	h := &healthScript{activeSeq: []string{"active"}, restartsSeq: []string{"0"}}
	stubHealth(t, h)
	orig := healthHTTPGet
	healthHTTPGet = func(string) (int, error) { return 500, nil }
	t.Cleanup(func() { healthHTTPGet = orig })

	_, err := DeployWithControl(a, "job-b17-url2", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil)
	if err == nil || !strings.Contains(err.Error(), "health URL") {
		t.Fatalf("err = %v, want a health URL failure", err)
	}
	assertFileBytes(t, a.BinaryPath, original)
	if h.slept > healthMaxWait+healthInterval {
		t.Errorf("waited %v, want about %v", h.slept, healthMaxWait)
	}
}

func TestHealth_NeverActiveGivesUpAfter30Seconds(t *testing.T) {
	a := testApp(t.TempDir())
	if err := os.WriteFile(a.BinaryPath, elfBinary("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	h := &healthScript{activeSeq: []string{"activating"}}
	stubHealth(t, h)
	_, err := DeployWithControl(a, "job-b17-never", "v2", progress.NewProgressTracker(), deployClientWithBody(elfBinary("new-binary")), nil)
	if err == nil {
		t.Fatal("a service stuck in activating must fail the deploy")
	}
	if h.slept != healthMaxWait {
		t.Errorf("virtual wait = %v, want exactly %v", h.slept, healthMaxWait)
	}
}

func TestValidateHealthURL(t *testing.T) {
	for _, ok := range []string{"", "http://127.0.0.1:8080/healthz", "https://example.com/up?x=1"} {
		if err := ValidateHealthURL(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"ftp://x/y", "javascript:alert(1)", "http://", "http://u:p@h/", "/relative", "http://a b/", "http://h/\nx", "http://" + strings.Repeat("a", maxHealthURLLen)} {
		if err := ValidateHealthURL(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}
