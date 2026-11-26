package admin

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/events"
	"github.com/izzamoe/auto-deploy/internal/httpx"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// T089 CR-10 (independent code review): RED before the fix, GREEN after
// (reference/executor/T089-red.log, T089.log).

// t089Publisher records what a handler publishes.
type t089Publisher struct {
	mu  sync.Mutex
	evs []events.Event
}

func (p *t089Publisher) PublishEvent(e events.Event) {
	p.mu.Lock()
	p.evs = append(p.evs, e)
	p.mu.Unlock()
}
func (p *t089Publisher) RefreshService(string) {}

// CR-10: "revoke other sessions" publishes account.changed{sessions} to the user's tabs.
func TestT089_CR10_RevokeAllPublishesAccountSessions(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "cr10.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	users, err := store.NewAdminUserStore(db)
	mutevMust(t, err)
	sessions, err := store.NewAdminSessionStore(db)
	mutevMust(t, err)
	user, err := users.CreateUser("admin", sessionTestPassword, false)
	mutevMust(t, err)
	mgr, err := auth.NewManager(auth.SessionConfig{Key: bytes.Repeat([]byte{0x10}, 32), Secure: true}, users, sessions)
	mutevMust(t, err)
	iss, err := mgr.Issue(user, "127.0.0.1", "cr10")
	mutevMust(t, err)
	pub := &t089Publisher{}
	account := NewAccountHandler(users, mgr, "")
	account.SetEventPublisher(pub)
	h := server.New()
	h.Use(httpx.RequestID())
	RegisterAccountRoutesHertz(h, account, auth.Middleware(auth.MiddlewareConfig{Sessions: mgr}))

	w := ut.PerformRequest(h.Engine, "POST", "http://app.test/admin/api/v1/account/sessions/revoke-all", &ut.Body{Body: strings.NewReader("{}"), Len: 2},
		ut.Header{Key: "Cookie", Value: mgr.CookieName() + "=" + iss.Token},
		ut.Header{Key: "X-Admin-Request", Value: "true"}, ut.Header{Key: "Sec-Fetch-Site", Value: "same-origin"},
		ut.Header{Key: "Content-Type", Value: "application/json"})
	if st := w.Result().StatusCode(); st != 200 {
		t.Fatalf("revoke-all = %d: %s", st, w.Result().Body())
	}
	pub.mu.Lock()
	defer pub.mu.Unlock()
	for _, ev := range pub.evs {
		if ev.Topic == events.TopicAccount && ev.Type == events.TypeAccountChanged && ev.UserID == user.ID {
			if sp, ok := ev.Payload.(events.SectionPayload); ok && sp.Section == events.SectionSessions {
				return
			}
		}
	}
	t.Fatalf("CR-10: revoke-all published %+v, want account.changed{sessions} for the user", pub.evs)
}
