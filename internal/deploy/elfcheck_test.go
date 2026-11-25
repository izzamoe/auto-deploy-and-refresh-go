package deploy

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/progress"
)

// B20 / FR-045 / PL-7: the artifact must be an ELF for THIS machine and, when the
// release publishes a digest, must hash to it.

// buildELF returns a minimal, parseable ELF64 little-endian file (64-byte header, no
// program or section headers) followed by payload.
func buildELF(machine elf.Machine, typ elf.Type, payload string) []byte {
	h := make([]byte, 64)
	copy(h, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(h[16:], uint16(typ))
	binary.LittleEndian.PutUint16(h[18:], uint16(machine))
	binary.LittleEndian.PutUint32(h[20:], 1)
	binary.LittleEndian.PutUint16(h[52:], 64) // e_ehsize
	return append(h, []byte(payload)...)
}

func hostELFMachine() elf.Machine { return machineByGOARCH[runtime.GOARCH] }

func foreignMachine() elf.Machine {
	if runtime.GOARCH == "arm64" {
		return elf.EM_X86_64
	}
	return elf.EM_AARCH64
}

// digestResolver is an ArtifactResolver that also publishes the asset digest.
type digestResolver struct{ digest string }

func (digestResolver) ArtifactDownload(context.Context, string, string, string) (string, map[string]string, error) {
	return "https://example.invalid/artifact", nil, nil
}
func (d digestResolver) ArtifactDigest(context.Context, string, string, string) (string, error) {
	return d.digest, nil
}

func deployFixture(t *testing.T) (original []byte, run func(body []byte, resolver ArtifactResolver, mutate func()) error, binPath string) {
	t.Helper()
	app := testApp(t.TempDir())
	original = elfBinary("old-binary")
	if err := os.WriteFile(app.BinaryPath, original, 0755); err != nil {
		t.Fatal(err)
	}
	healthyServiceStub(t)
	run = func(body []byte, resolver ArtifactResolver, mutate func()) error {
		if mutate != nil {
			mutate()
		}
		_, err := DeployArtifact(app, "job-b20", "v2", progress.NewProgressTracker(), deployClientWithBody(body), nil, resolver)
		return err
	}
	return original, run, app.BinaryPath
}

// B20: an ELF built for another architecture must be rejected before it replaces the
// working binary.
func TestELF_ForeignArchitectureIsRejected(t *testing.T) {
	original, run, bin := deployFixture(t)
	err := run(buildELF(foreignMachine(), elf.ET_EXEC, "foreign"), nil, nil)
	if err == nil {
		t.Fatalf("B20: an ELF for %v was accepted on a %s host", foreignMachine(), runtime.GOARCH)
	}
	if !strings.Contains(err.Error(), "artifact_arch_mismatch") {
		t.Errorf("error %q must carry the code artifact_arch_mismatch", err.Error())
	}
	assertFileBytes(t, bin, original)
}

// B20: a digest that does not match the downloaded bytes must be rejected.
func TestELF_WrongDigestIsRejected(t *testing.T) {
	original, run, bin := deployFixture(t)
	body := elfBinary("new-binary")
	err := run(body, digestResolver{digest: "sha256:" + strings.Repeat("0", 64)}, nil)
	if err == nil {
		t.Fatal("B20: a wrong release digest was accepted")
	}
	if !strings.Contains(err.Error(), "artifact_digest_mismatch") {
		t.Errorf("error %q must carry the code artifact_digest_mismatch", err.Error())
	}
	assertFileBytes(t, bin, original)
}

func deployWithSummary(t *testing.T, requireDigest bool, body []byte, resolver ArtifactResolver) (summarySHA string, verified bool, err error) {
	t.Helper()
	app := testApp(t.TempDir())
	app.RequireDigest = requireDigest
	if werr := os.WriteFile(app.BinaryPath, elfBinary("old-binary"), 0755); werr != nil {
		t.Fatal(werr)
	}
	healthyServiceStub(t)
	sum, derr := DeployArtifact(app, "job-b20-x", "v2", progress.NewProgressTracker(), deployClientWithBody(body), nil, resolver)
	return sum.SHA256, sum.Verified, derr
}

// Q-14: a matching digest verifies the artifact.
func TestDigest_MatchIsVerified(t *testing.T) {
	body := elfBinary("new-binary")
	want := sha256.Sum256(body)
	sha, verified, err := deployWithSummary(t, true, body, digestResolver{digest: "SHA256:" + strings.ToUpper(hex.EncodeToString(want[:]))})
	if err != nil || !verified || sha != hex.EncodeToString(want[:]) {
		t.Fatalf("sha=%q verified=%v err=%v, want a verified deploy", sha, verified, err)
	}
}

// Q-14: no digest published -> continue with a warning and verified=false ...
func TestDigest_MissingContinuesUnverifiedForLegacyApps(t *testing.T) {
	sha, verified, err := deployWithSummary(t, false, elfBinary("new-binary"), digestResolver{digest: ""})
	if err != nil || verified || sha == "" {
		t.Fatalf("sha=%q verified=%v err=%v, want success with verified=false and the hash recorded", sha, verified, err)
	}
}

// ... unless the app requires a digest (require_digest, the default of NEW apps).
func TestDigest_MissingFailsWhenRequired(t *testing.T) {
	_, _, err := deployWithSummary(t, true, elfBinary("new-binary"), digestResolver{digest: ""})
	if err == nil || !strings.Contains(err.Error(), CodeArtifactDigestMissing) {
		t.Fatalf("err = %v, want %s", err, CodeArtifactDigestMissing)
	}
	// a resolver that cannot report digests at all behaves the same
	_, _, err = deployWithSummary(t, true, elfBinary("new-binary"), nil)
	if err == nil || !strings.Contains(err.Error(), CodeArtifactDigestMissing) {
		t.Fatalf("nil resolver: err = %v, want %s", err, CodeArtifactDigestMissing)
	}
}

func TestVerifyELF_RejectsNonExecutablesAndTruncatedFiles(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][]byte{
		"relocatable object": buildELF(hostELFMachine(), elf.ET_REL, "x"),
		"core file":          buildELF(hostELFMachine(), elf.ET_CORE, "x"),
		"truncated header":   {0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0},
		"magic only":         {0x7f, 'E', 'L', 'F'},
	}
	for name, content := range cases {
		path := dir + "/" + strings.ReplaceAll(name, " ", "_")
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		err := VerifyELF(path)
		var ae *ArtifactError
		if !errors.As(err, &ae) || ae.Code != CodeArtifactInvalidELF {
			t.Errorf("%s: err = %v, want %s", name, err, CodeArtifactInvalidELF)
		}
	}
	good := dir + "/good"
	_ = os.WriteFile(good, buildELF(hostELFMachine(), elf.ET_DYN, "x"), 0o644)
	if err := VerifyELF(good); err != nil {
		t.Errorf("an ET_DYN executable for this host must pass: %v", err)
	}
}
