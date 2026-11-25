package deploy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Input validation of an app's deployment-critical fields (FR-040, B7, B22, Q-11).
// binaryPath and serviceName end up in a systemd unit and in filesystem paths, and
// githubRepo/artifactName in the download URL, so they are checked at the API
// (field-level errors) and again by the unit generator's sanitisation.

const (
	maxBinaryPathLen   = 4096
	maxGithubRepoLen   = 200
	maxArtifactNameLen = 255
)

// binaryPathDenylist lists the system directories an app binary must not live in
// (Q-11, approved 2026-11-24). A path equal to one of them or below one is refused.
var binaryPathDenylist = []string{"/etc", "/usr/bin", "/usr/sbin", "/bin", "/sbin", "/boot", "/proc", "/sys", "/dev"}

var githubRepoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// hasControl reports whether s contains an ASCII control character (including NUL,
// CR, LF and tab) or DEL.
func hasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

// ValidateBinaryPath checks an app's binaryPath: absolute, already clean (no `.`,
// `..`, `//` or trailing slash), free of control characters, spaces and the
// characters systemd or a shell would interpret (`%` `$` `\`), and not on or under a
// system directory of binaryPathDenylist.
func ValidateBinaryPath(p string) error {
	switch {
	case p == "":
		return errors.New("binary path must not be empty")
	case len(p) > maxBinaryPathLen:
		return fmt.Errorf("binary path is too long (max %d bytes)", maxBinaryPathLen)
	case hasControl(p):
		return errors.New("binary path must not contain control characters")
	case strings.ContainsAny(p, " %$\\"):
		return errors.New("binary path must not contain spaces or the characters % $ \\")
	case !filepath.IsAbs(p):
		return errors.New("binary path must be absolute")
	case filepath.Clean(p) != p:
		return errors.New("binary path must be clean (no ., .., // or trailing slash)")
	case p == "/":
		return errors.New("binary path must name a file, not the root directory")
	}
	for _, dir := range binaryPathDenylist {
		if p == dir || strings.HasPrefix(p, dir+"/") {
			return fmt.Errorf("binary path must not be inside the system directory %s", dir)
		}
	}
	return nil
}

// ValidateBinaryPathNotSelf refuses a binaryPath that is the auto-deploy binary
// itself (compared after resolving symlinks when the files exist), because deploying
// over the running manager would replace it mid-deploy. selfPath is the manager's own
// executable path ("" = unknown, nothing to compare); allowSelf is true only for an
// app explicitly marked self-deploy (Q-19).
func ValidateBinaryPathNotSelf(binaryPath, selfPath string, allowSelf bool) error {
	if allowSelf || selfPath == "" {
		return nil
	}
	if filepath.Clean(binaryPath) == filepath.Clean(selfPath) {
		return errors.New("binary path must not be the auto-deploy binary itself")
	}
	a, errA := filepath.EvalSymlinks(binaryPath)
	b, errB := filepath.EvalSymlinks(selfPath)
	if errA == nil && errB == nil && a == b {
		return errors.New("binary path must not be the auto-deploy binary itself")
	}
	return nil
}

// ValidateServiceName checks an app's serviceName: "<name>" or "<name>.service" where
// <name> passes ValidateServiceUnitName (letters, digits, `_`, `-`; 1-100 characters;
// never a leading `-`, which systemctl would read as an option).
func ValidateServiceName(name string) error {
	_, _, err := serviceUnitBaseAndFile(name)
	return err
}

// ValidateGithubRepo checks "owner/repo": two components of [A-Za-z0-9_.-]+ and
// neither may be "." or "..", because the value is placed into a download URL path.
func ValidateGithubRepo(repo string) error {
	if len(repo) > maxGithubRepoLen {
		return fmt.Errorf("GitHub repo is too long (max %d)", maxGithubRepoLen)
	}
	if !githubRepoRe.MatchString(repo) {
		return errors.New("GitHub repo must look like owner/repo (letters, digits, . _ -)")
	}
	for _, part := range strings.Split(repo, "/") {
		if part == "." || part == ".." {
			return errors.New("GitHub repo must not contain . or .. components")
		}
	}
	return nil
}

// ValidateArtifactName checks the release asset name: no path separators, `..`,
// control characters, nor the URL metacharacters `? # %` (it becomes one URL path
// segment of the download URL).
func ValidateArtifactName(name string) error {
	switch {
	case name == "":
		return errors.New("artifact name must not be empty")
	case len(name) > maxArtifactNameLen:
		return fmt.Errorf("artifact name is too long (max %d)", maxArtifactNameLen)
	case hasControl(name):
		return errors.New("artifact name must not contain control characters")
	case strings.ContainsAny(name, "/\\"):
		return errors.New("artifact name must not contain path separators")
	case strings.Contains(name, ".."):
		return errors.New("artifact name must not contain ..")
	case strings.ContainsAny(name, "?#%"):
		return errors.New("artifact name must not contain the characters ? # %")
	}
	return nil
}

// AppInput is the part of an app request that ValidateAppInput judges.
type AppInput struct {
	BinaryPath   string
	ServiceName  string
	GithubRepo   string
	ArtifactName string
	// Optional per-app settings (FR-042, FR-047).
	RunAsUser string
	HealthURL string
}

// FieldError is one invalid field; Field uses the API (JSON) name.
type FieldError struct {
	Field   string
	Message string
}

// ValidateAppInput validates the format of every present field and returns one
// FieldError per invalid one. An empty field is the caller's "required" check and is
// not reported here.
func ValidateAppInput(in AppInput) []FieldError {
	var errs []FieldError
	check := func(field, value string, validate func(string) error) {
		if value == "" {
			return
		}
		if err := validate(value); err != nil {
			errs = append(errs, FieldError{Field: field, Message: err.Error()})
		}
	}
	check("binaryPath", in.BinaryPath, ValidateBinaryPath)
	check("serviceName", in.ServiceName, ValidateServiceName)
	check("githubRepo", in.GithubRepo, ValidateGithubRepo)
	check("artifactName", in.ArtifactName, ValidateArtifactName)
	check("runAsUser", in.RunAsUser, ValidateRunAsUser)
	check("healthUrl", in.HealthURL, ValidateHealthURL)
	return errs
}

// OwnExecutable returns the path of the running auto-deploy binary, or "" when it
// cannot be determined; pass it to ValidateBinaryPathNotSelf.
func OwnExecutable() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}
