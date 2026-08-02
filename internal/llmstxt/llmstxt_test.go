package llmstxt

import (
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

func newTestEngine(t *testing.T) *server.Hertz {
	t.Helper()
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	RegisterRoutesHertz(h)
	return h
}

func TestRoutesServePlainTextWithoutAuth(t *testing.T) {
	engine := newTestEngine(t)

	for _, path := range []string{"/llms.txt", "/llms-full.txt"} {
		w := ut.PerformRequest(engine.Engine, http.MethodGet, path, nil)
		resp := w.Result()

		if resp.StatusCode() != http.StatusOK {
			t.Fatalf("%s: status = %d, want %d", path, resp.StatusCode(), http.StatusOK)
		}
		if got := string(resp.Header.ContentType()); got != contentType {
			t.Errorf("%s: Content-Type = %q, want %q", path, got, contentType)
		}
		if len(resp.Body()) == 0 {
			t.Errorf("%s: empty body", path)
		}
		if got := string(resp.Header.Peek("Cache-Control")); got != "public, max-age=3600" {
			t.Errorf("%s: Cache-Control = %q", path, got)
		}
	}
}

func TestVersionPlaceholderIsSubstituted(t *testing.T) {
	for name, body := range map[string][]byte{"llms.txt": Index(), "llms-full.txt": Full()} {
		if strings.Contains(string(body), versionPlaceholder) {
			t.Errorf("%s still contains the raw %s placeholder", name, versionPlaceholder)
		}
		if !strings.Contains(string(body), "Version: "+Version) {
			t.Errorf("%s does not report the build version %q", name, Version)
		}
	}
}

// The placeholder tokens are declared twice — in cmd/llmsgen, which writes them,
// and here, where they are replaced. A typo in either set would ship a document
// telling an agent to build for GOARCH="{{GOARCH}}". Catch every unresolved token
// rather than only the ones this package knows about.
func TestNoPlaceholderSurvivesRendering(t *testing.T) {
	for name, body := range map[string][]byte{"llms.txt": Index(), "llms-full.txt": Full()} {
		s := string(body)
		for i := 0; i+1 < len(s); i++ {
			if s[i] != '{' || s[i+1] != '{' {
				continue
			}
			// `${{ ... }}` is a GitHub Actions expression in the workflow
			// examples and must survive verbatim — it is not our placeholder.
			if i > 0 && s[i-1] == '$' {
				continue
			}
			t.Errorf("%s contains an unresolved placeholder near: %q", name, s[i:min(i+40, len(s))])
		}
	}
}

// An agent builds against whatever architecture these documents report, so the
// reported value must be the one the server actually runs as.
func TestHostSectionReportsTheRunningPlatform(t *testing.T) {
	unameArch, rustTarget := archAliases(runtime.GOARCH)
	for name, body := range map[string][]byte{"llms.txt": Index(), "llms-full.txt": Full()} {
		for _, want := range []string{
			"| GOOS | `" + runtime.GOOS + "` |",
			"| GOARCH | `" + runtime.GOARCH + "` |",
			unameArch,
			rustTarget,
			"GOOS=" + runtime.GOOS + " GOARCH=" + runtime.GOARCH,
		} {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s does not report %q", name, want)
			}
		}
	}
}

func TestArchAliases(t *testing.T) {
	tests := []struct {
		goarch, wantUname, wantRust string
	}{
		{"amd64", "x86_64", "x86_64-unknown-linux-gnu"},
		{"arm64", "aarch64", "aarch64-unknown-linux-gnu"},
	}
	for _, tc := range tests {
		uname, rust := archAliases(tc.goarch)
		if uname != tc.wantUname || rust != tc.wantRust {
			t.Errorf("archAliases(%q) = (%q, %q), want (%q, %q)", tc.goarch, uname, rust, tc.wantUname, tc.wantRust)
		}
	}

	// An unsupported host must not be handed an invented target triple that would
	// fail confusingly inside cargo.
	if _, rust := archAliases("riscv64"); !strings.Contains(rust, "unsupported") {
		t.Errorf("archAliases(riscv64) invented a Rust target: %q", rust)
	}
}

// The llms.txt convention requires an H1 title followed by a blockquote summary;
// agents fetching the file rely on that shape to identify the project.
func TestIndexFollowsLLMsTxtShape(t *testing.T) {
	lines := strings.Split(strings.TrimSpace(string(Index())), "\n")
	if len(lines) < 3 {
		t.Fatalf("llms.txt is only %d lines", len(lines))
	}
	if !strings.HasPrefix(lines[0], "# ") {
		t.Errorf("first line = %q, want an H1 heading", lines[0])
	}
	if !strings.HasPrefix(lines[2], "> ") {
		t.Errorf("third line = %q, want a blockquote summary", lines[2])
	}
}

func TestIndexAdvertisesTheWebhookAndFullDocument(t *testing.T) {
	index := string(Index())
	for _, want := range []string{"/webhook", "/llms-full.txt", "| Method | Path | Auth |"} {
		if !strings.Contains(index, want) {
			t.Errorf("llms.txt does not mention %q", want)
		}
	}
}

// The generated content is what an agent will act on, so a stale route table is a
// correctness bug. This asserts the generator captured the endpoints it serves.
func TestFullDocumentCarriesCuratedSectionsAndRoutes(t *testing.T) {
	full := string(Full())
	for _, want := range []string{
		"Read this first",
		"Artifact contract",
		"The pipeline to generate",
		"Webhook reference",
		"Failure symptoms and their causes",
		"HTTP endpoints (generated from source)",
		"| POST | `/webhook` | bearer token |",
		"| GET | `/admin/api/apps` | admin session |",
	} {
		if !strings.Contains(full, want) {
			t.Errorf("llms-full.txt does not contain %q", want)
		}
	}
}

// The whole point of the index is to put the pipeline-relevant contract in front of
// an agent. Roughly fifty admin routes would bury the one endpoint CI can call, so
// the index must stay free of them.
func TestIndexOmitsAdminRoutes(t *testing.T) {
	for line := range strings.SplitSeq(string(Index()), "\n") {
		if strings.HasPrefix(line, "| ") && strings.Contains(line, "`/admin") {
			t.Errorf("index leaked an admin route into the endpoint table: %s", line)
		}
	}
}

// These are the constraints enforced in internal/deploy. An agent that does not see
// them here will publish an archive or a mis-sized asset and the deploy fails after
// the pipeline has already gone green.
func TestFullDocumentStatesTheArtifactConstraints(t *testing.T) {
	full := string(Full())
	for _, want := range []string{
		"ELF",
		"100 MiB",
		"releases/download/{tag}/{artifact",
		"chmod 0755",
		"GOOS=linux",
	} {
		if !strings.Contains(full, want) {
			t.Errorf("llms-full.txt does not state the artifact constraint %q", want)
		}
	}
}

// Publishing per-application secrets would turn a documentation endpoint into a
// credential leak, so guard the obvious markers.
func TestPublishedContentCarriesNoSecrets(t *testing.T) {
	body := strings.ToLower(string(Full()) + string(Index()))
	for _, forbidden := range []string{"your-secret-token-here", "admin_password=", "ghp_"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("published llms content contains %q", forbidden)
		}
	}
}
