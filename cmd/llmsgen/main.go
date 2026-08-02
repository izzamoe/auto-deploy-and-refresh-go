// Command llmsgen generates the llms.txt / llms-full.txt pair that the server
// publishes at /llms.txt and /llms-full.txt.
//
// The generated files are committed to the repository so that go:embed can pick
// them up during a normal build. `make llms-check` regenerates into a temporary
// directory and diffs against the committed copies, so CI fails whenever a route
// or a curated document changes without the published context being refreshed.
//
// Two inputs feed the output:
//
//   - docs/llms/*.md — hand-written context, ordered by filename prefix.
//   - the Go sources themselves — the HTTP route table is extracted from the
//     go/ast of every non-test file so it can never drift from what is served.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// VersionPlaceholder is substituted by the server at startup with the running
// build's version. Keeping the literal token in the generated file makes the
// committed artifact stable, so the CI drift check never trips on the version.
const VersionPlaceholder = "{{VERSION}}"

// Platform placeholders, resolved the same way. They must stay in lockstep with
// the constants in internal/llmstxt, which performs the substitution.
const (
	GOOSPlaceholder       = "{{GOOS}}"
	GOARCHPlaceholder     = "{{GOARCH}}"
	UnameArchPlaceholder  = "{{ARCH_UNAME}}"
	RustTargetPlaceholder = "{{RUST_TARGET}}"
)

// hostSection is emitted verbatim into both documents. The placeholders are
// resolved by the server at startup from its own runtime.GOOS/GOARCH, so the
// published file states the concrete architecture of the host that served it —
// removing the one build parameter an agent would otherwise have to ask about.
const hostSection = "## This host\n\n" +
	"Every application this server deploys runs on this same machine, so every artifact\n" +
	"must target the platform below. These values are read from the running server process\n" +
	"itself: it is an ELF binary the kernel here agreed to execute, so a binary built the\n" +
	"same way will run too.\n\n" +
	"| Property | Value |\n" +
	"| --- | --- |\n" +
	"| GOOS | `" + GOOSPlaceholder + "` |\n" +
	"| GOARCH | `" + GOARCHPlaceholder + "` |\n" +
	"| `uname -m` / `file` reports | `" + UnameArchPlaceholder + "` |\n" +
	"| Rust target triple | `" + RustTargetPlaceholder + "` |\n\n" +
	"Build command for a Go project — the `-o` name must be the app's configured artifact\n" +
	"name, which is the one value still to ask the owner for:\n\n" +
	"```bash\n" +
	"GOOS=" + GOOSPlaceholder + " GOARCH=" + GOARCHPlaceholder + " CGO_ENABLED=0 \\\n" +
	"  go build -trimpath -ldflags \"-s -w\" -o <ARTIFACT_NAME> ./cmd/app\n" +
	"```\n\n" +
	"Do not publish a multi-arch matrix. One application maps to exactly one artifact name,\n" +
	"so a second architecture would have to overwrite the same asset.\n\n"

var httpMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true,
}

type route struct {
	Method string
	Path   string
	Auth   bool
}

type section struct {
	// Slug is the file name without the numeric ordering prefix.
	Slug    string
	Title   string
	Summary string
	Body    string
}

func main() {
	var (
		root    = flag.String("root", ".", "repository root to scan")
		outDir  = flag.String("out", "internal/llmstxt", "directory to write llms.txt and llms-full.txt into")
		docsDir = flag.String("docs", "docs/llms", "directory of curated markdown sections, relative to root")
		repoURL = flag.String("repo-url", "https://github.com/izzamoe/auto-deploy-and-refresh-go", "canonical repository URL used for section links")
		ref     = flag.String("ref", "master", "git ref the section links point at")
	)
	flag.Parse()

	sections, err := loadSections(filepath.Join(*root, *docsDir))
	if err != nil {
		fail(err)
	}
	if len(sections) == 0 {
		fail(fmt.Errorf("no curated sections found in %s", filepath.Join(*root, *docsDir)))
	}

	routes, err := scanRoutes(*root, []string{"cmd", "internal"})
	if err != nil {
		fail(err)
	}
	if len(routes) == 0 {
		fail(fmt.Errorf("no HTTP routes found under %s; the AST scan is probably broken", *root))
	}

	index := renderIndex(sections, routes, *repoURL, *ref, *docsDir)
	full := renderFull(sections, routes)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fail(err)
	}
	if err := os.WriteFile(filepath.Join(*outDir, "llms.txt"), []byte(index), 0o644); err != nil {
		fail(err)
	}
	if err := os.WriteFile(filepath.Join(*outDir, "llms-full.txt"), []byte(full), 0o644); err != nil {
		fail(err)
	}

	fmt.Fprintf(os.Stderr, "llmsgen: wrote llms.txt (%d sections) and llms-full.txt (%d routes) to %s\n",
		len(sections), len(routes), *outDir)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "llmsgen:", err)
	os.Exit(1)
}

// loadSections reads every *.md file in dir. The first `# ` line becomes the
// title, the first `> ` line becomes the one-line summary used in the index, and
// everything else is carried verbatim into llms-full.txt.
func loadSections(dir string) ([]section, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	sections := make([]section, 0, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		s, err := parseSection(name, string(raw))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		sections = append(sections, s)
	}
	return sections, nil
}

func parseSection(name, raw string) (section, error) {
	s := section{Slug: name}
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	var body []string
	for _, line := range lines {
		switch {
		case s.Title == "" && strings.HasPrefix(line, "# "):
			s.Title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
		case s.Summary == "" && strings.HasPrefix(line, "> "):
			s.Summary = strings.TrimSpace(strings.TrimPrefix(line, "> "))
		default:
			body = append(body, line)
		}
	}
	if s.Title == "" {
		return s, fmt.Errorf("missing a `# Title` heading")
	}
	if s.Summary == "" {
		return s, fmt.Errorf("missing a `> summary` line")
	}
	s.Body = strings.TrimSpace(strings.Join(body, "\n"))
	return s, nil
}

// scanRoutes walks the Go sources and reconstructs the served route table from
// Hertz registration calls: `h.GET("/path", auth, handler)` and the group form
// `api := h.Group("/admin/api", auth); api.GET("/apps", handler)`.
func scanRoutes(root string, dirs []string) ([]route, error) {
	seen := map[string]route{}

	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			return scanFile(path, seen)
		})
		if err != nil {
			return nil, err
		}
	}

	routes := make([]route, 0, len(seen))
	for _, r := range seen {
		routes = append(routes, r)
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	return routes, nil
}

func scanFile(path string, seen map[string]route) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	// Group prefixes are file-local by construction in this codebase; each
	// Register*RoutesHertz function declares its own `api := h.Group(...)`.
	groups := map[string]struct {
		prefix string
		auth   bool
	}{}

	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Group" || len(call.Args) == 0 {
			return true
		}
		prefix, ok := stringLit(call.Args[0])
		if !ok {
			return true
		}
		groups[ident.Name] = struct {
			prefix string
			auth   bool
		}{prefix: prefix, auth: hasAuthArg(call.Args[1:])}
		return true
	})

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !httpMethods[sel.Sel.Name] || len(call.Args) == 0 {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		path, ok := stringLit(call.Args[0])
		if !ok {
			return true
		}
		// The /admin/api catch-alls exist only to return 404 for unknown API
		// paths; publishing them as endpoints would be misleading.
		if hasIdentArg(call.Args[1:], "notFound") {
			return true
		}

		r := route{Method: sel.Sel.Name, Path: path, Auth: hasAuthArg(call.Args[1:])}
		if g, isGroup := groups[recv.Name]; isGroup {
			r.Path = g.prefix + path
			r.Auth = r.Auth || g.auth
		}
		seen[r.Method+" "+r.Path] = r
		return true
	})

	return nil
}

func stringLit(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

func hasAuthArg(args []ast.Expr) bool { return hasIdentArg(args, "auth") }

func hasIdentArg(args []ast.Expr, name string) bool {
	for _, arg := range args {
		if ident, ok := arg.(*ast.Ident); ok && ident.Name == name {
			return true
		}
	}
	return false
}

func renderIndex(sections []section, routes []route, repoURL, ref, docsDir string) string {
	var b bytes.Buffer

	b.WriteString("# auto-deploy\n\n")
	b.WriteString("> Deployment service for Linux/systemd hosts. If you are an AI agent working in an application repository that deploys here, this file tells you how to build a CI/CD pipeline this server will accept: publish a GitHub Release whose asset is a raw Linux ELF binary named exactly as configured, then POST the tag to /webhook.\n\n")

	fmt.Fprintf(&b, "Version: %s\n", VersionPlaceholder)
	fmt.Fprintf(&b, "Repository: %s\n", repoURL)
	b.WriteString("Full context: /llms-full.txt\n\n")

	b.WriteString("Read the sections in order. The artifact contract is where pipelines usually break,\n")
	b.WriteString("and three of the values you need are configured per application in this server's\n")
	b.WriteString("admin UI — ask the repository owner for them rather than guessing.\n\n")

	b.WriteString(hostSection)

	b.WriteString("## Sections\n\n")
	for _, s := range sections {
		fmt.Fprintf(&b, "- [%s](%s/blob/%s/%s/%s): %s\n", s.Title, repoURL, ref, docsDir, s.Slug, s.Summary)
	}
	b.WriteString("\n")

	// The index deliberately lists only the endpoints an external caller can
	// reach. Dumping the ~50 admin routes here would bury the one endpoint a
	// pipeline actually needs; they remain in llms-full.txt for operators.
	b.WriteString("## Endpoints reachable from your pipeline\n\n")
	b.WriteString(renderRouteTable(externalRoutes(routes)))
	b.WriteString("\nEverything under `/admin` requires an admin session cookie and is not usable from CI.\n")
	b.WriteString("The complete route table is in /llms-full.txt.\n")

	b.WriteString("\n## Optional\n\n")
	fmt.Fprintf(&b, "- [README](%s/blob/%s/README.md): install, upgrade, and release-contract details\n", repoURL, ref)
	fmt.Fprintf(&b, "- [Release contract](%s/blob/%s/release-contract.sh): machine-readable asset names and install paths\n", repoURL, ref)

	return b.String()
}

func renderFull(sections []section, routes []route) string {
	var b bytes.Buffer

	b.WriteString("# auto-deploy — full context\n\n")
	fmt.Fprintf(&b, "Version: %s\n\n", VersionPlaceholder)
	b.WriteString("Generated by cmd/llmsgen. Do not edit by hand — edit docs/llms/*.md and run `make llms`.\n\n")

	b.WriteString(hostSection)

	for _, s := range sections {
		fmt.Fprintf(&b, "---\n\n## %s\n\n> %s\n\n%s\n\n", s.Title, s.Summary, s.Body)
	}

	b.WriteString("---\n\n## HTTP endpoints (generated from source)\n\n")
	b.WriteString("Extracted from the Hertz route registrations at build time, so this table always\n")
	b.WriteString("matches what the binary actually serves.\n\n")
	b.WriteString(renderRouteTable(routes))

	return b.String()
}

// externalRoutes keeps only endpoints a caller outside the admin UI can use.
// Filtering on the /admin prefix rather than on the auth flag is deliberate:
// /admin/login is technically unauthenticated but is still useless to a pipeline.
func externalRoutes(routes []route) []route {
	external := make([]route, 0, 4)
	for _, r := range routes {
		if !strings.HasPrefix(r.Path, "/admin") {
			external = append(external, r)
		}
	}
	return external
}

func renderRouteTable(routes []route) string {
	var b bytes.Buffer
	b.WriteString("| Method | Path | Auth |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, r := range routes {
		auth := "public"
		switch {
		case r.Auth:
			auth = "admin session"
		case r.Path == "/webhook":
			auth = "bearer token"
		}
		fmt.Fprintf(&b, "| %s | `%s` | %s |\n", r.Method, r.Path, auth)
	}
	return b.String()
}
