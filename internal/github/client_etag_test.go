package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// T047 (FR-010, research.md section 9): the release list of a repo is cached for 60 s, then
// revalidated with If-None-Match; a 304 keeps the cached list. An expired entry (a new job
// of an app) is revalidated at once. A token change drops the cache; a failure is never
// cached.

type etagServer struct {
	mu       sync.Mutex
	etag     string
	body     string
	status   int
	requests []http.Header
}

func (s *etagServer) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Header.Clone())
	if s.status != 0 {
		w.WriteHeader(s.status)
		return
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" && inm == s.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", s.etag)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(s.body))
}

func (s *etagServer) calls() []http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]http.Header(nil), s.requests...)
}

func (s *etagServer) set(etag, body string) {
	s.mu.Lock()
	s.etag, s.body = etag, body
	s.mu.Unlock()
}

func newETagClient(t *testing.T, s *etagServer) (*Client, *time.Time) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	orig := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() { apiBaseURL = orig })
	c := NewClient("tok")
	now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	return c, &now
}

const releasesV1 = `[{"tag_name":"v1.0.0","assets":[{"name":"app-linux"}]}]`
const releasesV2 = `[{"tag_name":"v2.0.0","assets":[{"name":"app-linux"}]},{"tag_name":"v1.0.0","assets":[{"name":"app-linux"}]}]`

func tags(t *testing.T, c *Client) []string {
	t.Helper()
	rs, err := c.ListReleases(context.Background(), "owner", "repo")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rs {
		out = append(out, r.TagName)
	}
	return out
}

func TestReleasesETag_TTLThenIfNoneMatch(t *testing.T) {
	s := &etagServer{etag: `"e1"`, body: releasesV1}
	c, now := newETagClient(t, s)

	if got := tags(t, c); len(got) != 1 || got[0] != "v1.0.0" {
		t.Fatalf("first list = %v", got)
	}
	*now = now.Add(59 * time.Second)
	tags(t, c)
	if n := len(s.calls()); n != 1 {
		t.Fatalf("a second list within 60 s made %d requests, want 1 (served from the cache)", n)
	}

	*now = now.Add(2 * time.Second) // past the TTL: revalidate
	if got := tags(t, c); len(got) != 1 {
		t.Fatalf("after a 304 the list = %v, want the cached one", got)
	}
	calls := s.calls()
	if len(calls) != 2 || calls[1].Get("If-None-Match") != `"e1"` || calls[1].Get("Authorization") != "Bearer tok" {
		t.Fatalf("revalidation request headers = %v, want If-None-Match \"e1\" with the token", calls[len(calls)-1])
	}
	// The 304 renewed the entry: another TTL without requests.
	*now = now.Add(30 * time.Second)
	tags(t, c)
	if n := len(s.calls()); n != 2 {
		t.Fatalf("after the 304 renewal %d requests, want 2", n)
	}

	// A new release: the revalidation gets 200 with the new list and ETag.
	s.set(`"e2"`, releasesV2)
	*now = now.Add(time.Minute)
	if got := tags(t, c); len(got) != 2 || got[0] != "v2.0.0" {
		t.Fatalf("after a change the list = %v, want v2.0.0 first", got)
	}
	*now = now.Add(time.Minute)
	tags(t, c)
	if calls := s.calls(); calls[len(calls)-1].Get("If-None-Match") != `"e2"` {
		t.Fatalf("the new ETag is not used: %v", calls[len(calls)-1])
	}
}

func TestReleasesETag_ExpireRevalidatesAtOnce(t *testing.T) {
	s := &etagServer{etag: `"e1"`, body: releasesV1}
	c, _ := newETagClient(t, s)
	tags(t, c)
	c.ExpireReleases("owner", "other") // another repo: no effect
	tags(t, c)
	if n := len(s.calls()); n != 1 {
		t.Fatalf("expiring another repo caused a request (%d)", n)
	}
	c.ExpireReleases("owner", "repo")
	s.set(`"e2"`, releasesV2)
	if got := tags(t, c); len(got) != 2 {
		t.Fatalf("after ExpireReleases the list = %v, want the new one at once", got)
	}
	if calls := s.calls(); len(calls) != 2 || calls[1].Get("If-None-Match") != `"e1"` {
		t.Fatalf("ExpireReleases must revalidate with the old ETag: %v", calls)
	}
}

func TestReleasesETag_TokenChangeAndErrorsAreNotCached(t *testing.T) {
	s := &etagServer{etag: `"e1"`, body: releasesV1}
	c, _ := newETagClient(t, s)
	tags(t, c)
	c.SetToken("tok") // the same token keeps the cache
	tags(t, c)
	if n := len(s.calls()); n != 1 {
		t.Fatalf("setting the same token dropped the cache (%d requests)", n)
	}
	c.SetToken("other")
	tags(t, c)
	calls := s.calls()
	if len(calls) != 2 || calls[1].Get("If-None-Match") != "" || calls[1].Get("Authorization") != "Bearer other" {
		t.Fatalf("after a token change: %d requests, last %v; want a plain request with the new token", len(calls), calls[len(calls)-1])
	}

	s.mu.Lock()
	s.status = http.StatusForbidden
	s.mu.Unlock()
	c.ExpireReleases("owner", "repo")
	if _, err := c.ListReleases(context.Background(), "owner", "repo"); err == nil {
		t.Fatal("a 403 was answered from the cache")
	}
	s.mu.Lock()
	s.status = 0
	s.mu.Unlock()
	if got := tags(t, c); len(got) != 1 {
		t.Fatalf("after the failure the list = %v", got)
	}
	// Callers may change what they get without touching the cache.
	rs, _ := c.ListReleases(context.Background(), "owner", "repo")
	rs[0].TagName = "mutated"
	if got := tags(t, c); got[0] != "v1.0.0" {
		t.Fatalf("a caller's change leaked into the cache: %v", got)
	}
}
