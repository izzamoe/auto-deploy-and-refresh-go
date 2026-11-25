package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// T047 (B7, B22, FR-040, Q-11): input validation of an app's binaryPath, serviceName,
// githubRepo and artifactName.

func TestValidateBinaryPath(t *testing.T) {
	t.Parallel()
	ok := []string{
		"/opt/app/app",
		"/srv/my-app/bin/server",
		"/home/deploy/apps/app_1/app.v2",
		"/usr/local/bin/app", // /usr/local/bin is NOT on the denylist
		"/opt/etc/app",       // "etc" below another directory is fine
		"/opt/bin2/app",
	}
	for _, p := range ok {
		if err := ValidateBinaryPath(p); err != nil {
			t.Errorf("ValidateBinaryPath(%q) = %v, want nil", p, err)
		}
	}
	bad := map[string]string{
		"":                     "empty",
		"app":                  "relative",
		"./app":                "relative",
		"opt/app":              "relative",
		"/opt/app/../../etc/x": "not clean (..)",
		"/opt//app":            "not clean (//)",
		"/opt/./app":           "not clean (.)",
		"/opt/app/":            "trailing slash",
		"/":                    "root",
		"/opt/my app/app":      "space",
		"/opt/app\t/app":       "tab",
		"/opt/x\nExecStartPre=/bin/sh -c id\n/bin": "newline (B7 payload)",
		"/opt/x\r/app":                  "carriage return",
		"/opt/x\x00/app":                "NUL",
		"/opt/100%h/app":                "percent (systemd specifier)",
		"/opt/$HOME/app":                "dollar (systemd variable)",
		`/opt/back\slash/app`:           "backslash",
		"/etc/app":                      "denylist /etc",
		"/etc/cron.d/x":                 "under /etc",
		"/usr/bin/app":                  "denylist /usr/bin",
		"/usr/sbin/app":                 "denylist /usr/sbin",
		"/bin/sh":                       "denylist /bin",
		"/sbin/init":                    "denylist /sbin",
		"/boot/vmlinuz":                 "denylist /boot",
		"/proc/self/exe":                "denylist /proc",
		"/sys/kernel/x":                 "denylist /sys",
		"/dev/null":                     "denylist /dev",
		"/etc":                          "the denylisted directory itself",
		"/" + strings.Repeat("a", 4100): "too long",
	}
	for p, why := range bad {
		if err := ValidateBinaryPath(p); err == nil {
			t.Errorf("ValidateBinaryPath(%q) = nil, want an error (%s)", p, why)
		}
	}
}

func TestValidateBinaryPathNotSelf(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "auto-deploy")
	if err := os.WriteFile(self, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "alias")
	if err := os.Symlink(self, link); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other")

	if err := ValidateBinaryPathNotSelf(self, self, false); err == nil {
		t.Error("an app pointing at the auto-deploy binary itself must be refused")
	}
	if err := ValidateBinaryPathNotSelf(link, self, false); err == nil {
		t.Error("a symlink to the auto-deploy binary must be refused too")
	}
	if err := ValidateBinaryPathNotSelf(self, self, true); err != nil {
		t.Errorf("a self-deploy app (Q-19) may point at it: %v", err)
	}
	if err := ValidateBinaryPathNotSelf(other, self, false); err != nil {
		t.Errorf("another path is fine: %v", err)
	}
	if err := ValidateBinaryPathNotSelf(other, "", false); err != nil {
		t.Errorf("unknown own path must not block: %v", err)
	}
}

func TestValidateServiceName(t *testing.T) {
	t.Parallel()
	for _, n := range []string{"app.service", "app", "my-app_1.service", "A1.service"} {
		if err := ValidateServiceName(n); err != nil {
			t.Errorf("ValidateServiceName(%q) = %v, want nil", n, err)
		}
	}
	for _, n := range []string{
		"", ".service", "-x.service", "--now.service", "-", "a b.service", "a/b.service", "../x.service",
		"a.timer", "a.service.service", "a;b.service", "a\nb.service", "a%b.service", "a$b.service",
		strings.Repeat("a", 101) + ".service",
	} {
		if err := ValidateServiceName(n); err == nil {
			t.Errorf("ValidateServiceName(%q) = nil, want an error", n)
		}
	}
}

func TestValidateGithubRepo(t *testing.T) {
	t.Parallel()
	for _, r := range []string{"owner/repo", "o/r", "my-org/my.repo_1", "A1/b2"} {
		if err := ValidateGithubRepo(r); err != nil {
			t.Errorf("ValidateGithubRepo(%q) = %v, want nil", r, err)
		}
	}
	for _, r := range []string{
		"", "owner", "owner/", "/repo", "a/b/c", "owner/repo/", "https://github.com/a/b", "own er/repo",
		"../repo", "owner/..", "./repo", "owner/.", "a/b?x=1", "a/b#frag", "a/b%2f", "a\\b/c", "a/b\n",
	} {
		if err := ValidateGithubRepo(r); err == nil {
			t.Errorf("ValidateGithubRepo(%q) = nil, want an error", r)
		}
	}
}

func TestValidateArtifactName(t *testing.T) {
	t.Parallel()
	for _, n := range []string{"app", "app-linux-amd64", "app_1.0.tar.gz", "my.app"} {
		if err := ValidateArtifactName(n); err != nil {
			t.Errorf("ValidateArtifactName(%q) = %v, want nil", n, err)
		}
	}
	for _, n := range []string{
		"", "a/b", "/abs", "..", "a..b", "../x", "a\nb", "a\x00b", "a\tb", "a\\b", "a?b", "a#b", "a%2fb",
		strings.Repeat("a", 300),
	} {
		if err := ValidateArtifactName(n); err == nil {
			t.Errorf("ValidateArtifactName(%q) = nil, want an error", n)
		}
	}
}

func TestValidateAppInput(t *testing.T) {
	t.Parallel()
	good := AppInput{BinaryPath: "/opt/a/a", ServiceName: "a.service", GithubRepo: "o/r", ArtifactName: "a"}
	if errs := ValidateAppInput(good); len(errs) != 0 {
		t.Fatalf("valid input rejected: %v", errs)
	}
	bad := AppInput{BinaryPath: "/etc/x", ServiceName: "-x", GithubRepo: "nope", ArtifactName: "a/b"}
	errs := ValidateAppInput(bad)
	got := map[string]bool{}
	for _, e := range errs {
		got[e.Field] = true
		if e.Message == "" {
			t.Errorf("field %q has an empty message", e.Field)
		}
	}
	for _, f := range []string{"binaryPath", "serviceName", "githubRepo", "artifactName"} {
		if !got[f] {
			t.Errorf("no error reported for field %q (got %v)", f, errs)
		}
	}
	// Empty fields are the caller's "required" check; the validators only judge present values.
	if errs := ValidateAppInput(AppInput{}); len(errs) != 0 {
		t.Errorf("empty input must not produce format errors, got %v", errs)
	}
}
