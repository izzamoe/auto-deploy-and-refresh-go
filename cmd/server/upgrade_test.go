package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// B21 / FR-046: `auto-deploy upgrade` must not pipe a script fetched from the master
// branch into a shell. The source is inspected so the test keeps meaning after the
// implementation is replaced.
func TestUpgrade_DoesNotPipeMasterScriptToShell(t *testing.T) {
	src, err := os.ReadFile("upgrade.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if strings.Contains(text, "/master/") {
		t.Error("B21: upgrade.go fetches a script from the moving master branch (/master/)")
	}
	if strings.Contains(text, "| sh") {
		t.Error("B21: upgrade.go pipes a downloaded script straight into a shell (| sh)")
	}
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fakeRelease serves a GitHub-like API and download endpoint for owner/repo.
type fakeRelease struct {
	tag     string
	assets  map[string][]byte
	mu      sync.Mutex
	hits    []string
	latest  bool
	srv     *httptest.Server
	baseURL string
}

func newFakeRelease(t *testing.T, tag string, assets map[string][]byte) *fakeRelease {
	f := &fakeRelease{tag: tag, assets: assets}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits = append(f.hits, r.URL.Path)
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/repos/o/r/releases/latest":
			f.latest = true
			fmt.Fprintf(w, `{"tag_name": %q}`, f.tag)
		case strings.HasPrefix(r.URL.Path, "/o/r/releases/download/"):
			parts := strings.Split(r.URL.Path, "/")
			name := parts[len(parts)-1]
			if parts[len(parts)-2] != f.tag {
				http.NotFound(w, r)
				return
			}
			if data, ok := f.assets[name]; ok {
				_, _ = w.Write(data)
				return
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	f.baseURL = f.srv.URL
	return f
}

func (f *fakeRelease) config(run func(script, tag, sums string) error) upgradeConfig {
	return upgradeConfig{
		Repo: "o/r", APIBase: f.baseURL, DownloadBase: f.baseURL,
		HTTP: f.srv.Client(), Stdout: io.Discard, Stderr: io.Discard,
		RunScript: func(script, tag, sums string, _, _ io.Writer) error { return run(script, tag, sums) },
	}
}

func releaseAssets(script []byte, sums string) map[string][]byte {
	return map[string][]byte{"install.sh": script, "SHA256SUMS": []byte(sums)}
}

func TestUpgrade_VerifiedScriptRunsForThePinnedTag(t *testing.T) {
	script := []byte("#!/bin/sh\necho installing\n")
	f := newFakeRelease(t, "v1.2.3", releaseAssets(script, sum(script)+"  install.sh\n"))
	var ranTag, ranBody string
	err := upgradeFlow(context.Background(), f.config(func(path, tag, sums string) error {
		b, _ := os.ReadFile(path)
		ranTag, ranBody = tag, string(b)
		if _, err := os.Stat(sums); err != nil {
			t.Errorf("the verified SHA256SUMS must be handed to the script: %v", err)
		}
		return nil
	}), "v1.2.3")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if ranTag != "v1.2.3" || ranBody != string(script) {
		t.Errorf("ran tag=%q body=%q", ranTag, ranBody)
	}
	if f.latest {
		t.Error("a pinned tag must not query the latest release")
	}
}

func TestUpgrade_LatestIsResolvedThroughTheAPI(t *testing.T) {
	script := []byte("#!/bin/sh\n")
	f := newFakeRelease(t, "v2.0.0", releaseAssets(script, strings.ToUpper(sum(script))+" *install.sh\n"))
	var ranTag string
	if err := upgradeFlow(context.Background(), f.config(func(_, tag, _ string) error { ranTag = tag; return nil }), ""); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if ranTag != "v2.0.0" || !f.latest {
		t.Errorf("tag=%q latestQueried=%v", ranTag, f.latest)
	}
}

// FR-046: a wrong checksum aborts before anything is executed.
func TestUpgrade_WrongChecksumRunsNothing(t *testing.T) {
	script := []byte("#!/bin/sh\necho evil\n")
	f := newFakeRelease(t, "v1.2.3", releaseAssets(script, strings.Repeat("0", 64)+"  install.sh\n"))
	called := false
	err := upgradeFlow(context.Background(), f.config(func(_, _, _ string) error { called = true; return nil }), "v1.2.3")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
	if called {
		t.Fatal("the install script ran although its checksum did not match")
	}
}

func TestUpgrade_MissingChecksumEntryOrFileRunsNothing(t *testing.T) {
	script := []byte("#!/bin/sh\n")
	for name, assets := range map[string]map[string][]byte{
		"no entry for install.sh": releaseAssets(script, sum([]byte("x"))+"  other.zip\n"),
		"no SHA256SUMS":           {"install.sh": script},
		"no install.sh":           {"SHA256SUMS": []byte(sum(script) + "  install.sh\n")},
	} {
		f := newFakeRelease(t, "v1.2.3", assets)
		called := false
		err := upgradeFlow(context.Background(), f.config(func(_, _, _ string) error { called = true; return nil }), "v1.2.3")
		if err == nil || called {
			t.Errorf("%s: err=%v called=%v, want a failure without running anything", name, err, called)
		}
	}
}

func TestUpgrade_RejectsMalformedTags(t *testing.T) {
	f := newFakeRelease(t, "v1.0.0", nil)
	for _, tag := range []string{"master", "v1.0.0; rm -rf /", "../v1", "v1 2", "1.2.3", "v" + strings.Repeat("9", 80)} {
		if err := upgradeFlow(context.Background(), f.config(func(_, _, _ string) error { return nil }), tag); err == nil {
			t.Errorf("tag %q was accepted", tag)
		}
	}
	if len(f.hits) != 0 {
		t.Errorf("a malformed tag must not reach the network: %v", f.hits)
	}
}

// ---- install.sh against a local release server (FR-046): the archive is verified
// against SHA256SUMS before anything is unpacked or installed.

type installEnv struct {
	dir, installDir string
	env             []string
}

func newInstallScriptEnv(t *testing.T, srvURL string) installEnv {
	t.Helper()
	for _, bin := range []string{"unzip", "sha256sum", "sh", "install", "mktemp"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	fakeID := write("fake-id", "#!/bin/sh\necho 0\n")
	fakeSystemctl := write("fake-systemctl", "#!/bin/sh\nexit 0\n")
	installDir := filepath.Join(dir, "opt")
	return installEnv{dir: dir, installDir: installDir, env: append(os.Environ(),
		"AUTO_DEPLOY_REPO=o/r",
		"AUTO_DEPLOY_GITHUB_API_BASE="+srvURL,
		"AUTO_DEPLOY_GITHUB_DOWNLOAD_BASE="+srvURL,
		"AUTO_DEPLOY_GITHUB_RAW_BASE="+srvURL,
		"AUTO_DEPLOY_ID_BIN="+fakeID,
		"AUTO_DEPLOY_SYSTEMCTL_BIN="+fakeSystemctl,
		"AUTO_DEPLOY_SYSTEMD_DIR="+dir,
		"AUTO_DEPLOY_INSTALL_DIR="+installDir,
		"AUTO_DEPLOY_BINARY_PATH="+filepath.Join(installDir, "auto-deploy"),
		"AUTO_DEPLOY_SERVICE_UNIT_PATH="+filepath.Join(dir, "auto-deploy.service"),
		"AUTO_DEPLOY_ENV_PATH="+filepath.Join(dir, "auto-deploy.env"),
	)}
}

func zipOf(t *testing.T, name string, body []byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(body)
	_ = zw.Close()
	return buf.Bytes()
}

func runInstallScriptFor(t *testing.T, env installEnv, extra ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "../../install.sh", "v1.2.3")
	cmd.Env = append(append([]string{}, env.env...), extra...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func installAssets(t *testing.T, sums func(archive []byte) string) map[string][]byte {
	archive := zipOf(t, "auto-deploy", []byte("\x7fELFnew"))
	assets := map[string][]byte{
		"auto-deploy_linux_amd64.zip": archive,
		"auto-deploy_linux_arm64.zip": archive,
		"auto-deploy.service":         []byte("[Service]\nExecStart=/bin/true\n"),
		"auto-deploy.env.example":     []byte("A=1\n"),
	}
	if sums != nil {
		assets["SHA256SUMS"] = []byte(sums(archive))
	}
	return assets
}

func serveInstallAssets(t *testing.T, assets map[string][]byte) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if data, ok := assets[name]; ok {
			_, _ = w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestInstallScript_WrongChecksumInstallsNothing(t *testing.T) {
	assets := installAssets(t, func([]byte) string {
		return strings.Repeat("0", 64) + "  auto-deploy_linux_amd64.zip\n" + strings.Repeat("0", 64) + "  auto-deploy_linux_arm64.zip\n"
	})
	env := newInstallScriptEnv(t, serveInstallAssets(t, assets))
	out, err := runInstallScriptFor(t, env)
	if err == nil || !strings.Contains(out, "checksum mismatch") {
		t.Fatalf("err = %v, output = %s; want a checksum mismatch failure", err, out)
	}
	if _, statErr := os.Stat(filepath.Join(env.installDir, "auto-deploy")); statErr == nil {
		t.Error("the binary was installed although the checksum did not match")
	}
}

func TestInstallScript_MissingSHA256SUMSRefusesUnlessOverridden(t *testing.T) {
	env := newInstallScriptEnv(t, serveInstallAssets(t, installAssets(t, nil)))
	out, err := runInstallScriptFor(t, env)
	if err == nil || !strings.Contains(out, "SHA256SUMS") {
		t.Fatalf("err = %v, output = %s; want a refusal without SHA256SUMS", err, out)
	}
	if _, statErr := os.Stat(filepath.Join(env.installDir, "auto-deploy")); statErr == nil {
		t.Error("installed without checksum verification")
	}
	out, err = runInstallScriptFor(t, env, "AUTO_DEPLOY_ALLOW_UNVERIFIED=1")
	if err != nil || !strings.Contains(out, "WARNING") {
		t.Fatalf("explicit override: err = %v, output = %s", err, out)
	}
}

func TestInstallScript_CorrectChecksumInstalls(t *testing.T) {
	assets := installAssets(t, func(archive []byte) string {
		return strings.ToUpper(sum(archive)) + "  auto-deploy_linux_amd64.zip\n" + sum(archive) + "  auto-deploy_linux_arm64.zip\n"
	})
	env := newInstallScriptEnv(t, serveInstallAssets(t, assets))
	out, err := runInstallScriptFor(t, env)
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(env.installDir, "auto-deploy")); string(got) != "\x7fELFnew" {
		t.Errorf("installed binary = %q", got)
	}
}
