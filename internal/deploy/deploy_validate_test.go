package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// T047 (FR-040, defense in depth): the runner refuses an unsafe app row BEFORE it
// downloads, writes a file or calls systemctl.
func TestDeploy_RefusesUnsafeAppBeforeTouchingAnything(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name  string
		mod   func(*store.App)
		field string
	}{
		{"service name starting with a dash", func(a *store.App) { a.ServiceName = "--now.service" }, "serviceName"},
		{"binary path with a newline", func(a *store.App) { a.BinaryPath = filepath.Join(dir, "x\nExecStartPre=/bin/id") }, "binaryPath"},
		{"binary path under /etc", func(a *store.App) { a.BinaryPath = "/etc/auto-deploy-test-binary" }, "binaryPath"},
		{"artifact name with a slash", func(a *store.App) { a.ArtifactName = "../x" }, "artifactName"},
		{"repo that is a URL", func(a *store.App) { a.GithubRepo = "https://evil.example/x" }, "githubRepo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := testApp(dir)
			tc.mod(app)
			called := false
			stubSystemctl(t, func(string, ...string) ([]byte, error) { called = true; return nil, nil })
			tracker := progress.NewProgressTracker()
			downloads := 0
			client := fakeBodyClientCounting(&downloads)

			_, err := DeployArtifact(app, "job-unsafe", "v1", tracker, client, nil, nil)
			if err == nil || !strings.Contains(err.Error(), "edit app: "+tc.field+" is invalid") {
				t.Fatalf("error = %v, want 'edit app: %s is invalid ...'", err, tc.field)
			}
			if called {
				t.Error("systemctl must not be called for an unsafe app")
			}
			if downloads != 0 {
				t.Errorf("%d downloads started, want none", downloads)
			}
			if _, statErr := os.Stat(app.BinaryPath + ".tmp"); statErr == nil {
				t.Error("a temp file was written for an unsafe app")
			}
			if snap, ok := tracker.Snapshot(app.ID); !ok || snap.Phase != progress.ProgressStageFailed {
				t.Errorf("tracker = %+v ok=%v, want terminal phase failed", snap, ok)
			}
		})
	}
}
