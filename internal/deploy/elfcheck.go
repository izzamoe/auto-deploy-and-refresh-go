package deploy

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/izzamoe/auto-deploy/internal/store"
)

// Stable error codes of artifact verification (FR-045). The message of the returned
// error starts with the code.
const (
	CodeArtifactArchMismatch   = "artifact_arch_mismatch"
	CodeArtifactInvalidELF     = "artifact_invalid_elf"
	CodeArtifactDigestMismatch = "artifact_digest_mismatch"
	CodeArtifactDigestMissing  = "artifact_digest_missing"
)

// ArtifactError is a verification failure with a stable code.
type ArtifactError struct {
	Code string
	Msg  string
}

func (e *ArtifactError) Error() string { return e.Code + ": " + e.Msg }

// ArtifactDigester is implemented by an ArtifactResolver that can also report the
// release asset's published digest ("sha256:<hex>"); an empty digest means the release
// did not publish one.
type ArtifactDigester interface {
	ArtifactDigest(ctx context.Context, repo, tag, artifact string) (string, error)
}

var machineByGOARCH = map[string]elf.Machine{
	"amd64":   elf.EM_X86_64,
	"arm64":   elf.EM_AARCH64,
	"386":     elf.EM_386,
	"arm":     elf.EM_ARM,
	"riscv64": elf.EM_RISCV,
	"ppc64le": elf.EM_PPC64,
	"ppc64":   elf.EM_PPC64,
	"s390x":   elf.EM_S390,
}

// VerifyELF checks that path is an ELF executable (ET_EXEC or ET_DYN) built for the
// machine this process runs on (B20, FR-045).
func VerifyELF(path string) error {
	f, err := elf.Open(path)
	if err != nil {
		return &ArtifactError{CodeArtifactInvalidELF, "the artifact is not a valid ELF file"}
	}
	defer f.Close()
	if f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN {
		return &ArtifactError{CodeArtifactInvalidELF, fmt.Sprintf("unsupported ELF type %v (want an executable)", f.Type)}
	}
	want, known := machineByGOARCH[runtime.GOARCH]
	if !known {
		slog.Warn("no ELF machine known for this GOARCH; skipping the architecture check", "goarch", runtime.GOARCH)
		return nil
	}
	if f.Machine != want {
		return &ArtifactError{CodeArtifactArchMismatch, fmt.Sprintf("the artifact is built for %v but this host is %s (%v)", f.Machine, runtime.GOARCH, want)}
	}
	return nil
}

// FileSHA256 returns the lowercase hex SHA-256 of the file.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// parseSHA256Digest accepts "sha256:<64 hex>" (any case); other forms are not usable.
func parseSHA256Digest(d string) (string, bool) {
	algo, hexsum, ok := strings.Cut(strings.TrimSpace(d), ":")
	if !ok || !strings.EqualFold(algo, "sha256") || len(hexsum) != 64 {
		return "", false
	}
	if _, err := hex.DecodeString(hexsum); err != nil {
		return "", false
	}
	return strings.ToLower(hexsum), true
}

// verifyArtifact is the verification step of a deploy: ELF + architecture, then the
// release digest. It returns the file's SHA-256 and whether the digest was checked.
// Q-14: a release without a digest proceeds with a warning (verified=false) unless the
// app requires one.
func verifyArtifact(ctx context.Context, app *store.App, resolver ArtifactResolver, tag, path string) (sum string, verified bool, err error) {
	if err := VerifyELF(path); err != nil {
		return "", false, err
	}
	sum, err = FileSHA256(path)
	if err != nil {
		return "", false, fmt.Errorf("hash artifact: %w", err)
	}

	expected := ""
	var lookupErr error
	if d, ok := resolver.(ArtifactDigester); ok {
		expected, lookupErr = d.ArtifactDigest(ctx, app.GithubRepo, tag, app.ArtifactName)
	}
	want, usable := parseSHA256Digest(expected)
	if !usable {
		reason := "the release asset publishes no sha256 digest"
		if lookupErr != nil {
			reason = "the release digest could not be looked up"
			slog.Warn("artifact digest lookup failed", "app", app.ID, "err", lookupErr)
		}
		if app.RequireDigest {
			return sum, false, &ArtifactError{CodeArtifactDigestMissing, reason + "; this app requires a verified digest"}
		}
		slog.Warn("deploying an artifact without digest verification", "app", app.ID, "tag", tag, "reason", reason)
		return sum, false, nil
	}
	if sum != want {
		return sum, false, &ArtifactError{CodeArtifactDigestMismatch, "the downloaded file does not match the digest published by the release"}
	}
	return sum, true, nil
}

var _ = errors.New
