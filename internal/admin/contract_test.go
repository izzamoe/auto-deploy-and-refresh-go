package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/github"
	"github.com/izzamoe/auto-deploy/internal/httpx"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
	"github.com/izzamoe/auto-deploy/internal/telegram"
)

// The contract tests drive the REAL handlers (real stores on a temp SQLite file, the
// real auth middleware) and validate every request and response against
// api/admin-api.v1.openapi.yaml with kin-openapi (FR-027, SC-005).

const contractSpecPath = "../../api/admin-api.v1.openapi.yaml"

// knownDeviations lists responses where the real code does not (yet) match the
// contract, keyed "operationId status". Each entry names the task that closes it.
// A deviation that is not listed here fails the test; a listed one that no longer
// occurs also fails, so the list cannot rot.
var knownDeviations = map[string]string{}

// contractCoverage records, across every environment of a test run, which operations
// got a case and which known deviations were actually observed.
type contractCoverage struct {
	mu      sync.Mutex
	covered map[string]bool
	seenDev map[string]bool
}

func newContractCoverage() *contractCoverage {
	return &contractCoverage{covered: map[string]bool{}, seenDev: map[string]bool{}}
}

func (c *contractCoverage) markCovered(op string) {
	c.mu.Lock()
	c.covered[op] = true
	c.mu.Unlock()
}

func (c *contractCoverage) markDeviation(key string) {
	c.mu.Lock()
	c.seenDev[key] = true
	c.mu.Unlock()
}

type contractEnv struct {
	t      *testing.T
	doc    *openapi3.T
	router routers.Router
	h      *server.Hertz
	cookie string
	mgr    *auth.Manager
	apps   *store.AppStore
	queue  *store.DeployQueue
	cov    *contractCoverage
	users  *store.AdminUserStore
	lister *contractReleaseLister
}

// contractReleaseLister is a GitHub stand-in whose answer the test chooses.
type contractReleaseLister struct {
	releases []github.Release
	err      error
}

func (l *contractReleaseLister) ListReleases(context.Context, string, string) ([]github.Release, error) {
	return l.releases, l.err
}

func newContractEnv(t *testing.T, cov *contractCoverage) *contractEnv {
	t.Helper()
	ctx := context.Background()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(contractSpecPath)
	if err != nil {
		t.Fatalf("load OpenAPI: %v", err)
	}
	if err := doc.Validate(ctx); err != nil {
		t.Fatalf("the OpenAPI document is not valid: %v", err)
	}
	// The contract declares the relative server "/"; the router matches that against
	// the request host, so drop it for routing (the file itself is not changed).
	doc.Servers = nil
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatalf("build contract router: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "contract.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	queue, err := store.NewDeployQueue(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Migrate(); err != nil {
		t.Fatal(err)
	}
	apps, err := store.NewAppStore(db)
	if err != nil {
		t.Fatal(err)
	}
	users, err := store.NewAdminUserStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	sessions, _ := store.NewAdminSessionStore(db)
	tokens, _ := store.NewAPITokenStore(db)
	envStore, err := store.NewAppEnvStore(db)
	if err != nil {
		t.Fatal(err)
	}
	argsStore, err := store.NewAppArgsStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ghStore, err := store.NewGitHubConfigStore(db)
	if err != nil {
		t.Fatal(err)
	}
	tgStore, err := store.NewTelegramConfigStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.CreateUser("admin", sessionTestPassword, false); err != nil {
		t.Fatal(err)
	}
	user, _ := users.GetByUsername("admin")
	mgr, err := auth.NewManager(auth.SessionConfig{Key: bytes.Repeat([]byte{6}, 32), Secure: true}, users, sessions)
	if err != nil {
		t.Fatal(err)
	}
	iss, err := mgr.Issue(user, "127.0.0.1", "contract-test")
	if err != nil {
		t.Fatal(err)
	}
	resolver := auth.NewClientIPResolver(nil)
	mw := auth.Middleware(auth.MiddlewareConfig{
		Sessions: mgr, Tokens: tokens, TokenLimiter: auth.NewWindow(10, time.Minute, nil), ClientIP: resolver,
	})

	h := server.New(server.WithDisablePrintRoute(true))
	h.Use(httpx.RequestID())
	RegisterSessionRoutesHertz(h, NewSessionHandler(SessionHandlerConfig{
		Sessions: mgr, Users: users, Limiter: auth.NewLoginLimiter(nil), ClientIP: resolver,
	}), mw)
	RegisterAccountRoutesHertz(h, NewAccountHandler(users, mgr, ""), mw)
	RegisterTokenRoutesHertz(h, NewTokensHandler(tokens), mw)
	RegisterAppEnvRoutesHertz(h, NewAppEnvHandler(apps, envStore), mw)
	RegisterAppArgsRoutesHertz(h, NewAppArgsHandler(apps, argsStore), mw)
	RegisterGitHubRoutesHertz(h, NewGitHubConfigHandler(ghStore, contractTokenSetter{}, ""), mw)
	tg := NewTelegramConfigHandler(t.Context(), tgStore, filepath.Join(t.TempDir(), "tg.json"), &fakeNotifierSetter{})
	tg.newManager = func(telegram.Config) telegramManagerLifecycle { return &fakeTelegramManager{} }
	RegisterTelegramRoutesHertz(h, tg, mw)
	RegisterServiceUnitRoutesHertz(h, NewServiceUnitAdminHandler(apps, envStore, argsStore), mw)
	lister := &contractReleaseLister{}
	RegisterReleasesRoutesHertz(h, NewReleasesHandler(apps, lister), mw)
	RegisterSystemRoutesHertz(h, NewSystemLogsHandler("auto-deploy.service"), mw)
	RegisterAdminAPIRoutesHertz(h, NewAdminAPIHandler(apps, queue, progress.NewProgressTracker(), cancel.NewCancelService(queue)), mw)

	return &contractEnv{
		t: t, doc: doc, router: router, h: h, mgr: mgr, apps: apps, queue: queue,
		cookie: mgr.CookieName() + "=" + iss.Token, cov: cov, users: users, lister: lister,
	}
}

type contractCall struct {
	op     string // expected operationId
	method string
	path   string // concrete path with query
	body   any    // JSON-encoded when non-nil; a string is sent verbatim
	// noCookie sends the request without a session; headers adds/overrides headers.
	noCookie bool
	headers  map[string]string
	status   int // expected status
	// invalid marks a deliberately bad request (to provoke a 4xx): the request is
	// not checked against the contract, the RESPONSE still is.
	invalid bool
}

// do sends the request through the real handlers and validates both directions
// against the contract. It returns the status and the raw body.
func (e *contractEnv) do(c contractCall) (int, []byte) {
	e.t.Helper()
	var payload []byte
	switch b := c.body.(type) {
	case nil:
	case string:
		payload = []byte(b)
	default:
		var err error
		if payload, err = json.Marshal(b); err != nil {
			e.t.Fatal(err)
		}
	}
	hdrs := map[string]string{}
	if !c.noCookie {
		hdrs["Cookie"] = e.cookie
		if c.method != http.MethodGet {
			hdrs["X-Admin-Request"] = "true"
			hdrs["Sec-Fetch-Site"] = "same-origin"
		}
	}
	if payload != nil || (c.method != http.MethodGet && !c.noCookie) {
		hdrs["Content-Type"] = "application/json"
	}
	for k, v := range c.headers {
		hdrs[k] = v
	}
	var uh []ut.Header
	for k, v := range hdrs {
		uh = append(uh, ut.Header{Key: k, Value: v})
	}
	var body *ut.Body
	if payload != nil {
		body = &ut.Body{Body: bytes.NewReader(payload), Len: len(payload)}
	}
	w := ut.PerformRequest(e.h.Engine, c.method, "http://app.test"+c.path, body, uh...)
	res := w.Result()
	status, respBody := res.StatusCode(), append([]byte(nil), res.Body()...)

	if status != c.status {
		e.t.Errorf("%s %s: status %d, want %d (body %s)", c.method, c.path, status, c.status, respBody)
	}

	// Validate against the contract.
	u, _ := url.Parse("http://app.test" + c.path)
	httpReq, _ := http.NewRequest(c.method, u.String(), bytes.NewReader(payload))
	for k, v := range hdrs {
		httpReq.Header.Set(k, v)
	}
	route, pathParams, err := e.router.FindRoute(httpReq)
	if err != nil {
		e.t.Errorf("%s %s is not described by the contract: %v", c.method, c.path, err)
		return status, respBody
	}
	if route.Operation == nil || route.Operation.OperationID != c.op {
		got := ""
		if route.Operation != nil {
			got = route.Operation.OperationID
		}
		e.t.Errorf("%s %s maps to operation %q, test expected %q", c.method, c.path, got, c.op)
	}
	e.cov.markCovered(c.op)
	reqIn := &openapi3filter.RequestValidationInput{
		Request: httpReq, PathParams: pathParams, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}
	if !c.invalid {
		if err := openapi3filter.ValidateRequest(context.Background(), reqIn); err != nil {
			e.t.Errorf("%s %s: the TEST request violates the contract: %v", c.method, c.path, err)
		}
	}
	respIn := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: reqIn, Status: status,
		Header:  http.Header{"Content-Type": []string{string(res.Header.ContentType())}},
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}
	respIn.SetBodyBytes(respBody)
	key := c.op + " " + itoa(status)
	if err := openapi3filter.ValidateResponse(context.Background(), respIn); err != nil {
		if reason, ok := knownDeviations[key]; ok {
			e.cov.markDeviation(key)
			e.t.Logf("KNOWN DEVIATION %s (%s): %v", key, reason, err)
		} else {
			e.t.Errorf("%s %s -> %d violates the contract: %v\nbody: %s", c.method, c.path, status, err, respBody)
		}
	} else if _, ok := knownDeviations[key]; ok {
		e.t.Errorf("%s is listed as a known deviation but now matches the contract; delete it from knownDeviations", key)
	}
	return status, respBody
}

// contractTokenSetter satisfies githubTokenSetter without a live GitHub client.
type contractTokenSetter struct{}

func (contractTokenSetter) SetToken(string) {}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func jsonField(t *testing.T, raw []byte, path ...string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not JSON: %s", raw)
	}
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("path %v not found in %s", path, raw)
		}
		cur = mm[p]
	}
	s, _ := cur.(string)
	return s
}

// operationsOf lists every operationId of the document with its method and path.
func operationsOf(doc *openapi3.T) map[string][2]string {
	out := map[string][2]string{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op.OperationID != "" {
				out[op.OperationID] = [2]string{method, path}
			}
		}
	}
	return out
}

// implementedOperations returns the operationIds whose method+path exist as real
// routes in the engine (the catch-all 404 routes do not count).
func (e *contractEnv) implementedOperations() []string {
	have := map[string]bool{}
	for _, ri := range e.h.Routes() {
		if strings.Contains(ri.Path, "*") {
			continue
		}
		have[ri.Method+" "+normalizeRoute(ri.Path)] = true
	}
	var impl []string
	for id, mp := range operationsOf(e.doc) {
		if have[mp[0]+" "+normalizeRoute(mp[1])] {
			impl = append(impl, id)
		}
	}
	sort.Strings(impl)
	return impl
}

// normalizeRoute turns ":id" and "{appId}" into "{}" so Hertz and OpenAPI templates compare.
func normalizeRoute(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		if strings.HasPrefix(s, ":") || (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) {
			parts[i] = "{}"
		}
	}
	return strings.Join(parts, "/")
}
