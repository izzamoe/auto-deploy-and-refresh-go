package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/coder/websocket"
	"github.com/izzamoe/auto-deploy/internal/admission"
	"github.com/izzamoe/auto-deploy/internal/auth"
	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/coordinator"
	"github.com/izzamoe/auto-deploy/internal/deploy"
	"github.com/izzamoe/auto-deploy/internal/httpx"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
	"github.com/izzamoe/auto-deploy/internal/telegram"
	"github.com/izzamoe/auto-deploy/internal/webhook"
)

// T015 (RED, FR-004, FR-005, FR-006): every row of the mutation table (plan.md section 4)
// pushes its event to the other tabs, on the topic of that row, within 1 s, and no frame
// ever carries a secret that was sent in the request (env values/names, args, webhook
// secret, API token, Telegram bot token/appHash, GitHub token, passwords).
//
// The environment is the server of cmd/server/main.go, built from the same constructors,
// with fakes only where a real system would be touched: the deploy runner (gated per tag),
// systemctl and the systemd unit directory (deploy test seams), GitHub (token setter) and
// the Telegram manager. Clients use protocol 3 (events-ws.v3.md): dial ?protocol=3, read
// `hello`, send `sub` for the topics of the subtest, then watch the frames.
//
// Subtests (RV2-1, fixed names; each asserts ONLY the events of its component so GREEN can
// arrive task by task): app_core (T040), job_core and cancel (T041), summary (T042), stats
// (T043), settings and account (T044), service (T045). MarkRolledBack, RecoverStale and
// PruneHistory are asserted by TestQueue_Observer (T041), not here (RV3-13).
//
// Wiring contract (read this before making it GREEN): the constructors below are called
// exactly as cmd/server/main.go calls them today and the hub gets the same explicit wiring
// (tracker progress sink, cancel/coordinator event sink, session checker). Everything 002
// adds is connected by mutevWire through SETTERS, so this file does not need to change
// when the publishers arrive:
//   - every component method Set<X>(p) whose name ends in Sink, Hub, Publisher, Observer,
//     Bus or Events, whose single parameter is not the empty interface and is satisfied by
//     *AdminEventHub, is called with the hub (e.g. a handler's SetEventPublisher(hub), the
//     queue's SetJobObserver(hub));
//   - every hub method Set<X>(p) (other than SetServerVersion/SetSessionChecker) whose single
//     non-empty parameter type is satisfied by exactly ONE component of the environment is
//     called with that component (e.g. hub.SetAppStore(apps)).
func TestMutationEvents_Table(t *testing.T) {
	t.Run("app_core", func(t *testing.T) {
		e := newMutevEnv(t)
		s := e.dial(t, e.cookie)
		s.sub(t, "r1", nil, mutevTopic("apps"))

		webhookSecret := "Mv7qT2xK9pL4wZ8rB3nH6jD1fG5sC0aE"
		envName, envValue := "MUTEV_SECRET_NAME", "mutev-env-value-7f3a9b21"
		envValue2 := "mutev-env-value-2-0c4e8d"
		argValue, argValue2 := "--mutev-arg-value-5c1d", "--mutev-arg-value-2-9e7b"

		m := s.mark()
		id := e.createApp(t, map[string]any{
			"name": "mutev-app", "binaryPath": "/opt/mutev/app", "serviceName": "mutev-app.service",
			"githubRepo": "owner/mutev", "artifactName": "mutev-linux", "webhookSecret": webhookSecret,
			"envVars": []map[string]string{{"name": envName, "value": envValue}},
			"args":    []string{argValue},
		})
		s.expect(t, m, "POST /apps -> apps/app.upserted", mutevAppUpserted(id, nil))

		m = s.mark()
		e.mustDo(t, "PUT", "/apps/"+id, e.cookie, map[string]any{
			"name": "mutev-app-renamed", "binaryPath": "/opt/mutev/app", "serviceName": "mutev-app.service",
			"githubRepo": "owner/mutev", "artifactName": "mutev-linux",
		}, http.StatusOK)
		s.expect(t, m, "PUT /apps/:id -> apps/app.upserted", mutevAppUpserted(id, func(a map[string]any) bool { return a["name"] == "mutev-app-renamed" }))

		m = s.mark()
		e.mustDo(t, "POST", "/apps/"+id+"/toggle", e.cookie, map[string]bool{"enabled": false}, http.StatusOK)
		s.expect(t, m, "POST /apps/:id/toggle -> apps/app.upserted (enabled=false)", mutevAppUpserted(id, func(a map[string]any) bool { return a["enabled"] == false }))

		m = s.mark()
		_, rotated, _ := e.mustDo(t, "POST", "/apps/"+id+"/webhook-secret/rotate", e.cookie, nil, http.StatusOK)
		newSecret := mutevJSONString(t, rotated, "secret")
		s.expect(t, m, "POST /apps/:id/webhook-secret/rotate -> apps/app.upserted", mutevAppUpserted(id, nil))

		m = s.mark()
		e.mustDo(t, "PUT", "/apps/"+id+"/env", e.cookie, map[string]any{"envVars": []map[string]string{{"name": envName, "value": envValue2}}}, http.StatusOK)
		s.expect(t, m, "PUT /apps/:id/env -> apps/app.config_changed{part:env}", mutevConfigChanged(id, "env"))

		m = s.mark()
		e.mustDo(t, "PUT", "/apps/"+id+"/args", e.cookie, map[string]any{"args": []string{argValue2}}, http.StatusOK)
		s.expect(t, m, "PUT /apps/:id/args -> apps/app.config_changed{part:args}", mutevConfigChanged(id, "args"))

		m = s.mark()
		e.mustDo(t, "POST", "/apps/"+id+"/service-unit/apply", e.cookie, nil, http.StatusOK)
		s.expect(t, m, "POST /apps/:id/service-unit/apply -> apps/app.config_changed{part:unit}", mutevConfigChanged(id, "unit"))

		m = s.mark()
		e.mustDo(t, "DELETE", "/apps/"+id, e.cookie, nil, http.StatusOK)
		s.expect(t, m, "DELETE /apps/:id -> apps/app.deleted", func(f mutevFrame) bool {
			return f.Topic == "apps" && f.Type == "app.deleted" && f.AppID == id
		})

		mutevNoSecrets(t, []*mutevSocket{s}, map[string]string{
			"webhook secret": webhookSecret, "rotated webhook secret": newSecret,
			"env name": envName, "env value": envValue, "env value 2": envValue2,
			"args value": argValue, "args value 2": argValue2,
		})
	})

	t.Run("job_core", func(t *testing.T) {
		e := newMutevEnv(t)
		s := e.dial(t, e.cookie)
		s.sub(t, "r1", nil, mutevTopic("jobs"), mutevTopic("apps"))
		webhookSecret := "Jc4nR8vB2xM6qT0zK3wP7sL1dF5hG9aY"
		id := e.createApp(t, mutevAppBody("job", webhookSecret))

		// Manual deploy: pending (API), in_progress (worker DequeueNext), terminal (MarkDone).
		m := s.mark()
		e.mustDo(t, "POST", "/apps/"+id+"/deploy", e.cookie, map[string]string{"tag": "v1.0.0"}, http.StatusAccepted)
		job1 := e.jobID(t, id, "v1.0.0")
		s.expect(t, m, "POST /apps/:id/deploy -> jobs/job_status{pending}", mutevJobStatus(job1, "pending"))
		s.expect(t, m, "DequeueNext -> jobs/job_status{in_progress}", mutevJobStatus(job1, "in_progress"))
		m = s.mark()
		e.runner.finish("v1.0.0", nil)
		s.expect(t, m, "MarkDone -> jobs/job_status{succeeded}", mutevJobStatus(job1, "succeeded"))
		s.expect(t, m, "MarkDone -> apps/app.upserted (lastDeploy*)", mutevAppUpserted(id, nil))

		// Retry of the finished job: a new job with the same tag.
		m = s.mark()
		e.mustDo(t, "POST", "/history/"+job1+"/retry", e.cookie, nil, http.StatusAccepted)
		job2 := e.jobID(t, id, "v1.0.0")
		if job2 == job1 {
			t.Fatalf("retry did not create a new job (still %s)", job1)
		}
		s.expect(t, m, "POST /history/:id/retry -> jobs/job_status{pending}", mutevJobStatus(job2, "pending"))
		s.expect(t, m, "DequeueNext (retry) -> jobs/job_status{in_progress}", mutevJobStatus(job2, "in_progress"))
		m = s.mark()
		e.runner.finish("v1.0.0", errors.New("mutev: deploy failed"))
		s.expect(t, m, "MarkDone (failure) -> jobs/job_status{failed}", mutevJobStatus(job2, "failed"))

		// External webhook: the same three transitions.
		m = s.mark()
		e.webhook(t, webhookSecret, "v2.0.0")
		job3 := e.jobID(t, id, "v2.0.0")
		s.expect(t, m, "POST /webhook -> jobs/job_status{pending}", mutevJobStatus(job3, "pending"))
		s.expect(t, m, "DequeueNext (webhook) -> jobs/job_status{in_progress}", mutevJobStatus(job3, "in_progress"))
		m = s.mark()
		e.runner.finish("v2.0.0", nil)
		s.expect(t, m, "MarkDone (webhook job) -> jobs/job_status{succeeded}", mutevJobStatus(job3, "succeeded"))

		mutevNoSecrets(t, []*mutevSocket{s}, map[string]string{"webhook secret": webhookSecret})
	})

	t.Run("cancel", func(t *testing.T) {
		e := newMutevEnv(t)
		s := e.dial(t, e.cookie)
		s.sub(t, "r1", nil, mutevTopic("jobs"))
		id := e.createApp(t, mutevAppBody("cancel", "Cx5mW9pQ3tB7nV1rK4zH8sD2fJ6gL0aE"))

		// job1 runs (its runner is held), job2 waits behind it.
		e.mustDo(t, "POST", "/apps/"+id+"/deploy", e.cookie, map[string]string{"tag": "v1.0.0"}, http.StatusAccepted)
		job1 := e.jobID(t, id, "v1.0.0")
		e.waitJob(t, job1, "in_progress")
		e.mustDo(t, "POST", "/apps/"+id+"/deploy", e.cookie, map[string]string{"tag": "v2.0.0"}, http.StatusAccepted)
		job2 := e.jobID(t, id, "v2.0.0")

		m := s.mark()
		e.mustDo(t, "POST", "/jobs/"+job2+"/cancel", e.cookie, nil, http.StatusOK)
		s.expect(t, m, "POST /jobs/:id/cancel (queued) -> jobs/cancel_requested{status:canceled}", mutevJobEvent("cancel_requested", job2, "canceled"))
		s.expect(t, m, "POST /jobs/:id/cancel (queued) -> jobs/job_status{canceled}", mutevJobStatus(job2, "canceled"))

		m = s.mark()
		e.mustDo(t, "POST", "/apps/"+id+"/cancel", e.cookie, nil, http.StatusOK)
		s.expect(t, m, "POST /apps/:id/cancel (running) -> jobs/cancel_requested{status:cancel_requested}", mutevJobEvent("cancel_requested", job1, "cancel_requested"))
		e.runner.finish("v1.0.0", errors.New("mutev: canceled"))
	})

	t.Run("summary", func(t *testing.T) {
		e := newMutevEnv(t)
		s := e.dial(t, e.cookie)
		ack := s.sub(t, "r1", nil, mutevTopic("summary"))
		if st := mutevAckState(ack, "summary"); st == nil || st["appsTotal"] == nil {
			t.Fatalf("subscribed{summary} carries no state (events-ws.v3.md section 3): %s", ack.Raw)
		}
		secret := "Sm2kP6vR0xN4qW8tB1zJ5hL9dG3fC7aY"

		m := s.mark()
		id := e.createApp(t, mutevAppBody("summary", secret))
		s.expect(t, m, "POST /apps -> summary{1,1,0,0}", mutevSummary(1, 1, 0, 0))

		m = s.mark()
		e.mustDo(t, "POST", "/apps/"+id+"/toggle", e.cookie, map[string]bool{"enabled": false}, http.StatusOK)
		s.expect(t, m, "POST /apps/:id/toggle off -> summary{1,0,0,0}", mutevSummary(1, 0, 0, 0))
		m = s.mark()
		e.mustDo(t, "POST", "/apps/"+id+"/toggle", e.cookie, map[string]bool{"enabled": true}, http.StatusOK)
		s.expect(t, m, "POST /apps/:id/toggle on -> summary{1,1,0,0}", mutevSummary(1, 1, 0, 0))

		m = s.mark()
		e.mustDo(t, "POST", "/apps/"+id+"/deploy", e.cookie, map[string]string{"tag": "v1.0.0"}, http.StatusAccepted)
		job1 := e.jobID(t, id, "v1.0.0")
		e.waitJob(t, job1, "in_progress")
		s.expect(t, m, "deploy + DequeueNext -> summary{running:1}", mutevSummary(1, 1, 1, 0))

		m = s.mark()
		e.mustDo(t, "POST", "/apps/"+id+"/deploy", e.cookie, map[string]string{"tag": "v2.0.0"}, http.StatusAccepted)
		job2 := e.jobID(t, id, "v2.0.0")
		s.expect(t, m, "POST /apps/:id/deploy (app busy) -> summary{queued:1}", mutevSummary(1, 1, 1, 1))

		m = s.mark()
		e.mustDo(t, "POST", "/jobs/"+job2+"/cancel", e.cookie, nil, http.StatusOK)
		s.expect(t, m, "cancel queued job -> summary{queued:0}", mutevSummary(1, 1, 1, 0))

		m = s.mark()
		e.webhook(t, secret, "v3.0.0")
		s.expect(t, m, "POST /webhook (app busy) -> summary{queued:1}", mutevSummary(1, 1, 1, 1))

		m = s.mark()
		e.runner.finish("v1.0.0", nil)
		job3 := e.jobID(t, id, "v3.0.0")
		e.waitJob(t, job3, "in_progress")
		s.expect(t, m, "MarkDone + next DequeueNext -> summary{running:1,queued:0}", mutevSummary(1, 1, 1, 0))

		m = s.mark()
		e.runner.finish("v3.0.0", nil)
		e.waitJob(t, job3, "succeeded")
		s.expect(t, m, "MarkDone -> summary{running:0}", mutevSummary(1, 1, 0, 0))

		m = s.mark()
		e.mustDo(t, "POST", "/history/"+job1+"/retry", e.cookie, nil, http.StatusAccepted)
		s.expect(t, m, "POST /history/:id/retry -> summary with the retry queued or running", func(f mutevFrame) bool {
			return f.Topic == "summary" && f.Type == "summary" && mutevNum(f.Payload, "running")+mutevNum(f.Payload, "queued") == 1
		})
		job4 := e.jobID(t, id, "v1.0.0")
		e.waitJob(t, job4, "in_progress")
		m2 := s.mark()
		e.runner.finish("v1.0.0", nil)
		s.expect(t, m2, "MarkDone (retry) -> summary{running:0}", mutevSummary(1, 1, 0, 0))

		m = s.mark()
		e.waitJob(t, job4, "succeeded")
		e.mustDo(t, "DELETE", "/apps/"+id, e.cookie, nil, http.StatusOK)
		s.expect(t, m, "DELETE /apps/:id -> summary{0,0,0,0}", mutevSummary(0, 0, 0, 0))

		mutevNoSecrets(t, []*mutevSocket{s}, map[string]string{"webhook secret": secret})
	})

	t.Run("stats", func(t *testing.T) {
		e := newMutevEnv(t)
		s := e.dial(t, e.cookie)
		s.sub(t, "r1", nil, mutevTopic("stats"))

		m := s.mark()
		id := e.createApp(t, mutevAppBody("stats", "St8bN2wQ6rK0vM4xP7zH1jD5fL9gC3aE"))
		s.expect(t, m, "POST /apps -> stats/stats.changed{reasons:[app]}", mutevStatsChanged("app"))

		e.mustDo(t, "POST", "/apps/"+id+"/deploy", e.cookie, map[string]string{"tag": "v1.0.0"}, http.StatusAccepted)
		job1 := e.jobID(t, id, "v1.0.0")
		e.waitJob(t, job1, "in_progress")
		e.mustDo(t, "POST", "/apps/"+id+"/deploy", e.cookie, map[string]string{"tag": "v2.0.0"}, http.StatusAccepted)
		job2 := e.jobID(t, id, "v2.0.0")

		m = s.mark()
		e.mustDo(t, "POST", "/jobs/"+job2+"/cancel", e.cookie, nil, http.StatusOK)
		s.expect(t, m, "cancel queued job -> stats/stats.changed{reasons:[job]}", mutevStatsChanged("job"))

		m = s.mark()
		e.runner.finish("v1.0.0", nil)
		s.expect(t, m, "MarkDone -> stats/stats.changed{reasons:[job]}", mutevStatsChanged("job"))

		m = s.mark()
		e.waitJob(t, job1, "succeeded")
		e.mustDo(t, "DELETE", "/apps/"+id, e.cookie, nil, http.StatusOK)
		s.expect(t, m, "DELETE /apps/:id -> stats/stats.changed{reasons:[app]}", mutevStatsChanged("app"))
	})

	t.Run("settings", func(t *testing.T) {
		e := newMutevEnv(t)
		s := e.dial(t, e.cookie)
		s.sub(t, "r1", nil, mutevTopic("settings"))
		ghToken := "ghp_mutevMutevMutev0123456789abcdef"
		botToken, appHash := "123456:MUTEV-bot-token-9f8e7d", "mutevapphash0123456789abcdef"

		m := s.mark()
		e.mustDo(t, "POST", "/github", e.cookie, map[string]any{"token": ghToken}, http.StatusOK)
		s.expect(t, m, "POST /github -> settings/settings.changed{section:github}", mutevSection("settings", "settings.changed", "github"))

		m = s.mark()
		e.mustDo(t, "POST", "/telegram", e.cookie, map[string]any{
			"appId": 12345, "appHash": appHash, "botToken": botToken, "chatUsername": "@mutev", "enabled": true,
		}, http.StatusOK)
		s.expect(t, m, "POST /telegram -> settings/settings.changed{section:telegram}", mutevSection("settings", "settings.changed", "telegram"))

		mutevNoSecrets(t, []*mutevSocket{s}, map[string]string{"github token": ghToken, "telegram bot token": botToken, "telegram appHash": appHash})
	})

	t.Run("account", func(t *testing.T) {
		e := newMutevEnv(t)
		var sockets []*mutevSocket
		password := sessionTestPassword
		password2 := "mutev-new-password-4d2c"

		// Tokens: account.changed{tokens}; the token value only in the creator's HTTP response.
		s := e.dial(t, e.cookie)
		sockets = append(sockets, s)
		s.sub(t, "r1", nil, mutevTopic("account"))
		m := s.mark()
		_, created, _ := e.mustDo(t, "POST", "/tokens", e.cookie, map[string]string{"name": "mutev-ci"}, http.StatusCreated)
		tokenValue := mutevJSONString(t, created, "token")
		tokenID := mutevJSONString(t, created, "id")
		s.expect(t, m, "POST /tokens -> account/account.changed{section:tokens}", mutevSection("account", "account.changed", "tokens"))
		m = s.mark()
		e.mustDo(t, "DELETE", "/tokens/"+tokenID, e.cookie, nil, http.StatusNoContent)
		s.expect(t, m, "DELETE /tokens/:id -> account/account.changed{section:tokens}", mutevSection("account", "account.changed", "tokens"))

		// Password change: token_version moves, the socket of another session closes 4401.
		other := e.dial(t, e.issueSession(t))
		sockets = append(sockets, other)
		other.sub(t, "r1", nil, mutevTopic("account"))
		e.mustDo(t, "POST", "/account/password", e.cookie, map[string]string{"currentPassword": password, "newPassword": password2}, http.StatusOK)
		other.expectClose(t, 4401, "POST /account/password -> 4401 on another session's socket")

		// Revoke all: the socket of another session closes 4401.
		actor := e.issueSession(t)
		other2 := e.dial(t, e.issueSession(t))
		sockets = append(sockets, other2)
		other2.sub(t, "r1", nil, mutevTopic("account"))
		e.mustDo(t, "POST", "/account/sessions/revoke-all", actor, nil, http.StatusOK)
		other2.expectClose(t, 4401, "POST /account/sessions/revoke-all -> 4401 on another session's socket")

		// Username change (RV2-9): 4401 on another session's socket; the actor's tab gets
		// account.changed{profile}, live or by resume after it reconnects with its new cookie.
		actor3 := e.issueSession(t)
		sa := e.dial(t, actor3)
		sockets = append(sockets, sa)
		sa.sub(t, "r1", nil, mutevTopic("account"))
		epoch, _ := sa.Hello.Payload["epoch"].(string)
		helloSeq := mutevNum(sa.Hello.Payload, "seq")
		other3 := e.dial(t, e.issueSession(t))
		sockets = append(sockets, other3)
		other3.sub(t, "r1", nil, mutevTopic("account"))
		m = sa.mark()
		_, _, hdr := e.mustDo(t, "POST", "/account/username", actor3, map[string]string{"currentPassword": password2, "newUsername": "mutev-boss"}, http.StatusOK)
		newCookie := e.cookieFrom(t, hdr)
		other3.expectClose(t, 4401, "POST /account/username -> 4401 on another session's socket")
		profile := mutevSection("account", "account.changed", "profile")
		if !sa.has(m, profile, time.Second) {
			sa.expectClose(t, 4401, "POST /account/username -> the actor's own socket ends (token_version)")
			sr := e.dial(t, newCookie)
			sockets = append(sockets, sr)
			sr.sub(t, "r2", map[string]any{"epoch": epoch, "seq": helloSeq}, mutevTopic("account"))
			sr.expect(t, 0, "POST /account/username -> account/account.changed{section:profile} replayed after reconnect", profile)
		}

		// Logout: the socket of that session closes 4401.
		gone := e.issueSession(t)
		sg := e.dial(t, gone)
		sockets = append(sockets, sg)
		sg.sub(t, "r1", nil, mutevTopic("account"))
		e.mustDo(t, "DELETE", "/session", gone, nil, http.StatusNoContent)
		sg.expectClose(t, 4401, "DELETE /session -> 4401 on that session's socket")

		mutevNoSecrets(t, sockets, map[string]string{"api token": tokenValue, "password": password, "new password": password2})
	})

	t.Run("service", func(t *testing.T) {
		e := newMutevEnv(t)
		id := e.createApp(t, mutevAppBody("service", "Sv3cJ7nW1qR5xB9tM2zK6pH0dL4fG8aY"))
		unit := "mutev-service.service"
		e.systemctl.set(unit, "active")

		s := e.dial(t, e.cookie)
		ack := s.sub(t, "r1", nil, mutevTopic("service"))
		if !mutevServiceAckHas(ack, id, "active") {
			t.Fatalf("subscribed{service} must carry the state of every app (%s active): %s", id, ack.Raw)
		}

		m := s.mark()
		e.mustDo(t, "POST", "/apps/"+id+"/service/stop", e.cookie, nil, http.StatusOK)
		s.expect(t, m, "POST /apps/:id/service/stop -> service/service.status{inactive}", mutevServiceStatus(id, "inactive"))

		m = s.mark()
		e.systemctl.set(unit, "failed") // changed outside the panel; the apply must refresh at once
		e.mustDo(t, "POST", "/apps/"+id+"/service-unit/apply", e.cookie, nil, http.StatusOK)
		s.expect(t, m, "POST /apps/:id/service-unit/apply -> service/service.status{failed}", mutevServiceStatus(id, "failed"))

		e.mustDo(t, "POST", "/apps/"+id+"/deploy", e.cookie, map[string]string{"tag": "v1.0.0"}, http.StatusAccepted)
		job1 := e.jobID(t, id, "v1.0.0")
		e.waitJob(t, job1, "in_progress")
		m = s.mark()
		e.systemctl.set(unit, "active") // the deploy restarted the service
		e.runner.finish("v1.0.0", nil)
		s.expect(t, m, "MarkDone -> service/service.status{active}", mutevServiceStatus(id, "active"))
	})
}

// ---------------------------------------------------------------------------------------
// Environment

type mutevEnv struct {
	addr      string
	db        *sql.DB
	apps      *store.AppStore
	queue     *store.DeployQueue
	users     *store.AdminUserStore
	mgr       *auth.Manager
	user      *store.AdminUser
	hub       *AdminEventHub
	cookie    string // "name=value" of the main admin session
	runner    *mutevRunner
	systemctl *mutevSystemctl
}

func newMutevEnv(t *testing.T) *mutevEnv {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "mutev.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	queue, err := store.NewDeployQueue(db, 10)
	mutevMust(t, err)
	mutevMust(t, queue.Migrate())
	apps, err := store.NewAppStore(db)
	mutevMust(t, err)
	users, err := store.NewAdminUserStore(db)
	mutevMust(t, err)
	mutevMust(t, store.Migrate(db))
	sessions, err := store.NewAdminSessionStore(db)
	mutevMust(t, err)
	tokens, err := store.NewAPITokenStore(db)
	mutevMust(t, err)
	envStore, err := store.NewAppEnvStore(db)
	mutevMust(t, err)
	argsStore, err := store.NewAppArgsStore(db)
	mutevMust(t, err)
	ghStore, err := store.NewGitHubConfigStore(db)
	mutevMust(t, err)
	tgStore, err := store.NewTelegramConfigStore(db)
	mutevMust(t, err)

	// systemd stand-ins (package-level seams of internal/deploy; this test is not parallel).
	sc := &mutevSystemctl{state: map[string]string{}}
	restoreUnitDir := deploy.SetSystemdUnitDirForTest(t.TempDir())
	restoreSystemctl := deploy.SetRunSystemctlForTest(sc.run)
	t.Cleanup(func() {
		restoreSystemctl()
		restoreUnitDir()
	})

	ctx, stop := context.WithCancel(context.Background())
	adm := admission.NewAdmissionService(apps, queue)
	tracker := progress.NewProgressTracker()
	hub := NewAdminEventHub(tracker)
	hub.SetServerVersion("v0.0.0-mutev")
	tracker.SetProgressSink(hub)
	cancelService := cancel.NewCancelService(queue)
	cancelService.SetEventSink(hub)
	runner := &mutevRunner{ctx: ctx, gates: map[string]chan error{}}
	coord := coordinator.NewCoordinator(apps, queue, runner.run, tracker)
	coord.SetEventSink(hub)

	if _, err := users.CreateUser("admin", sessionTestPassword, false); err != nil {
		t.Fatal(err)
	}
	user, err := users.GetByUsername("admin")
	mutevMust(t, err)
	mgr, err := auth.NewManager(auth.SessionConfig{Key: bytes.Repeat([]byte{0x15}, 32), Secure: true}, users, sessions)
	mutevMust(t, err)
	hub.SetSessionChecker(mgr)
	resolver := auth.NewClientIPResolver(nil)
	mw := auth.Middleware(auth.MiddlewareConfig{
		Sessions: mgr, Tokens: tokens, TokenLimiter: auth.NewWindow(10, time.Minute, nil), ClientIP: resolver,
	})

	sessionHandler := NewSessionHandler(SessionHandlerConfig{Sessions: mgr, Users: users, Limiter: auth.NewLoginLimiter(nil), ClientIP: resolver})
	tokensHandler := NewTokensHandler(tokens)
	accountHandler := NewAccountHandler(users, mgr, "")
	tg := NewTelegramConfigHandler(ctx, tgStore, filepath.Join(dir, "telegram-session.json"), coord)
	tg.newManager = func(telegram.Config) telegramManagerLifecycle { return &fakeTelegramManager{} }
	ghHandler := NewGitHubConfigHandler(ghStore, contractTokenSetter{}, "")
	envHandler := NewAppEnvHandler(apps, envStore)
	argsHandler := NewAppArgsHandler(apps, argsStore)
	apiHandler := NewAdminAPIHandler(apps, queue, tracker, cancelService)
	releasesHandler := NewReleasesHandler(apps, &contractReleaseLister{})
	unitHandler := NewServiceUnitAdminHandler(apps, envStore, argsStore)
	systemHandler := NewSystemLogsHandler("auto-deploy.service")

	mutevWire(t, hub,
		apiHandler, envHandler, argsHandler, unitHandler, ghHandler, tg, tokensHandler, accountHandler,
		sessionHandler, releasesHandler, systemHandler,
		queue, apps, cancelService, coord, tracker, adm, mgr, users, sessions, tokens, envStore, argsStore, ghStore, tgStore,
	)

	addr := freeAddr(t)
	h := server.New(server.WithHostPorts(addr), server.WithDisablePrintRoute(true))
	h.Use(httpx.RequestID())
	RegisterSessionRoutesHertz(h, sessionHandler, mw)
	RegisterTokenRoutesHertz(h, tokensHandler, mw)
	h.POST("/webhook", webhook.HandlerWithLimits(adm, webhook.Limits{ClientIP: resolver}))
	RegisterAdminEventRoutesHertz(h, hub, mw)
	RegisterAccountRoutesHertz(h, accountHandler, mw)
	RegisterTelegramRoutesHertz(h, tg, mw)
	RegisterGitHubRoutesHertz(h, ghHandler, mw)
	RegisterAppEnvRoutesHertz(h, envHandler, mw)
	RegisterAppArgsRoutesHertz(h, argsHandler, mw)
	RegisterAdminAPIRoutesHertz(h, apiHandler, mw)
	RegisterReleasesRoutesHertz(h, releasesHandler, mw)
	RegisterServiceUnitRoutesHertz(h, unitHandler, mw)
	RegisterSystemRoutesHertz(h, systemHandler, mw)

	coord.Start(ctx)
	go h.Spin()
	t.Cleanup(func() {
		stop()
		coord.Wait()
		sctx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
		defer cancelShutdown()
		_ = h.Shutdown(sctx)
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mutation-events server did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}

	e := &mutevEnv{addr: addr, db: db, apps: apps, queue: queue, users: users, mgr: mgr, user: user, hub: hub, runner: runner, systemctl: sc}
	e.cookie = e.issueSession(t)
	return e
}

func mutevMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// mutevWire connects the hub to the components through setters (see the wiring contract
// at the top of TestMutationEvents_Table).
func mutevWire(t *testing.T, hub *AdminEventHub, components ...any) {
	t.Helper()
	hv := reflect.ValueOf(hub)
	suffixes := []string{"Sink", "Hub", "Publisher", "Observer", "Bus", "Events"}
	emptyIface := func(tp reflect.Type) bool { return tp.Kind() == reflect.Interface && tp.NumMethod() == 0 }
	for _, c := range components {
		cv := reflect.ValueOf(c)
		for i := 0; i < cv.NumMethod(); i++ {
			name := cv.Type().Method(i).Name
			if !strings.HasPrefix(name, "Set") {
				continue
			}
			matched := false
			for _, s := range suffixes {
				if strings.HasSuffix(name, s) {
					matched = true
				}
			}
			mt := cv.Method(i).Type()
			if !matched || mt.NumIn() != 1 || emptyIface(mt.In(0)) || !hv.Type().AssignableTo(mt.In(0)) {
				continue
			}
			cv.Method(i).Call([]reflect.Value{hv})
			t.Logf("wired %T.%s(hub)", c, name)
		}
	}
	for i := 0; i < hv.NumMethod(); i++ {
		name := hv.Type().Method(i).Name
		if !strings.HasPrefix(name, "Set") || name == "SetServerVersion" || name == "SetSessionChecker" {
			continue
		}
		mt := hv.Method(i).Type()
		if mt.NumIn() != 1 || emptyIface(mt.In(0)) {
			continue
		}
		var match []reflect.Value
		for _, c := range components {
			if cv := reflect.ValueOf(c); cv.Type().AssignableTo(mt.In(0)) {
				match = append(match, cv)
			}
		}
		switch len(match) {
		case 1:
			hv.Method(i).Call(match)
			t.Logf("wired hub.%s(%s)", name, match[0].Type())
		case 0:
		default:
			t.Logf("hub.%s: %d components fit %s; not wired (ambiguous)", name, len(match), mt.In(0))
		}
	}
}

// issueSession signs the admin in once more and returns the "name=value" cookie.
func (e *mutevEnv) issueSession(t *testing.T) string {
	t.Helper()
	user, err := e.users.GetByID(e.user.ID)
	mutevMust(t, err)
	iss, err := e.mgr.Issue(user, "127.0.0.1", "mutev-test")
	mutevMust(t, err)
	return e.mgr.CookieName() + "=" + iss.Token
}

func (e *mutevEnv) cookieFrom(t *testing.T, hdr http.Header) string {
	t.Helper()
	for _, line := range hdr.Values("Set-Cookie") {
		pair, _, _ := strings.Cut(line, ";")
		if name, value, ok := strings.Cut(pair, "="); ok && name == e.mgr.CookieName() && value != "" {
			return pair
		}
	}
	t.Fatalf("no %s cookie in the response: %v", e.mgr.CookieName(), hdr.Values("Set-Cookie"))
	return ""
}

// do sends one same-origin SPA request to /admin/api/v1<path>.
func (e *mutevEnv) do(t *testing.T, method, path, cookie string, body any) (int, []byte, http.Header) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		mutevMust(t, err)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, "http://"+e.addr+"/admin/api/v1"+path, rd)
	mutevMust(t, err)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-Admin-Request", "true")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, resp.Header
}

func (e *mutevEnv) mustDo(t *testing.T, method, path, cookie string, body any, want int) (int, []byte, http.Header) {
	t.Helper()
	status, raw, hdr := e.do(t, method, path, cookie, body)
	if status != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, status, want, raw)
	}
	return status, raw, hdr
}

func (e *mutevEnv) createApp(t *testing.T, body map[string]any) string {
	t.Helper()
	_, raw, _ := e.mustDo(t, "POST", "/apps", e.cookie, body, http.StatusCreated)
	var out struct {
		App struct {
			ID string `json:"id"`
		} `json:"app"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.App.ID == "" {
		t.Fatalf("create app: no id in %s (%v)", raw, err)
	}
	return out.App.ID
}

func mutevAppBody(name, webhookSecret string) map[string]any {
	return map[string]any{
		"name": "mutev-" + name, "binaryPath": "/opt/mutev-" + name + "/app", "serviceName": "mutev-" + name + ".service",
		"githubRepo": "owner/mutev-" + name, "artifactName": "mutev-" + name + "-linux", "webhookSecret": webhookSecret,
	}
}

func (e *mutevEnv) webhook(t *testing.T, secret, tag string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+e.addr+"/webhook", strings.NewReader(`{"tag":"`+tag+`"}`))
	mutevMust(t, err)
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	mutevMust(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /webhook %s = %d, want 202: %s", tag, resp.StatusCode, raw)
	}
}

// jobID is the newest job of the app with that tag.
func (e *mutevEnv) jobID(t *testing.T, appID, tag string) string {
	t.Helper()
	var id string
	if err := e.db.QueryRow(`SELECT id FROM deploy_jobs WHERE app_id = ? AND tag = ? ORDER BY seq DESC LIMIT 1`, appID, tag).Scan(&id); err != nil {
		t.Fatalf("job of %s/%s: %v", appID, tag, err)
	}
	return id
}

func (e *mutevEnv) waitJob(t *testing.T, jobID, status string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		if job, err := e.queue.GetJob(jobID); err == nil && job != nil {
			last = job.Status
			if last == status {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach %q (last %q)", jobID, status, last)
}

// mutevRunner stands in for the deploy: each tag's run waits until the test finishes it.
type mutevRunner struct {
	ctx   context.Context
	mu    sync.Mutex
	gates map[string]chan error
}

func (r *mutevRunner) gate(tag string) chan error {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.gates[tag]
	if !ok {
		g = make(chan error, 1)
		r.gates[tag] = g
	}
	return g
}

func (r *mutevRunner) run(_ *store.App, _ string, tag string) (store.DownloadSummary, error) {
	select {
	case err := <-r.gate(tag):
		return store.DownloadSummary{}, err
	case <-r.ctx.Done():
		return store.DownloadSummary{}, r.ctx.Err()
	}
}

func (r *mutevRunner) finish(tag string, err error) { r.gate(tag) <- err }

// mutevSystemctl answers like systemctl for the units the test set: is-active prints one
// state per unit (exit status 3 when one is not active), start/restart/stop change it.
type mutevSystemctl struct {
	mu    sync.Mutex
	state map[string]string
}

func (s *mutevSystemctl) set(unit, state string) {
	s.mu.Lock()
	s.state[unit] = state
	s.mu.Unlock()
}

func (s *mutevSystemctl) run(verb string, args ...string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var units []string
	for i, a := range args {
		if a == "--" {
			units = args[i+1:]
			break
		}
	}
	switch verb {
	case "is-active":
		var out strings.Builder
		allActive := true
		for _, u := range units {
			st, ok := s.state[u]
			if !ok {
				st = "inactive"
			}
			if st != "active" {
				allActive = false
			}
			out.WriteString(st + "\n")
		}
		if !allActive {
			return []byte(out.String()), errors.New("exit status 3")
		}
		return []byte(out.String()), nil
	case "start", "restart":
		for _, u := range units {
			s.state[u] = "active"
		}
	case "stop":
		for _, u := range units {
			s.state[u] = "inactive"
		}
	}
	return nil, nil
}

// ---------------------------------------------------------------------------------------
// Protocol-3 client

type mutevFrame struct {
	Raw      []byte
	Protocol string         `json:"protocol"`
	ID       string         `json:"id"`
	Seq      float64        `json:"seq"`
	Topic    string         `json:"topic"`
	Type     string         `json:"type"`
	AppID    string         `json:"appId"`
	JobID    string         `json:"jobId"`
	Payload  map[string]any `json:"payload"`
}

type mutevSocket struct {
	conn  *websocket.Conn
	Hello mutevFrame

	mu       sync.Mutex
	frames   []mutevFrame
	closeErr error
	done     chan struct{}
}

func mutevTopic(name string) map[string]any { return map[string]any{"name": name} }

// dial opens /admin/events/v1/ws?protocol=3 and reads the hello.
func (e *mutevEnv) dial(t *testing.T, cookie string) *mutevSocket {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws://"+e.addr+"/admin/events/v1/ws?protocol=3", &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {cookie}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial ?protocol=3: %v", err)
	}
	conn.SetReadLimit(1 << 20)
	t.Cleanup(func() { _ = conn.CloseNow() })
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("protocol 3: no hello (close status %d): %v", websocket.CloseStatus(err), err)
	}
	s := &mutevSocket{conn: conn, done: make(chan struct{})}
	if err := json.Unmarshal(raw, &s.Hello); err != nil {
		t.Fatalf("hello is not JSON: %v (%s)", err, raw)
	}
	s.Hello.Raw = raw
	if s.Hello.Type != "hello" || s.Hello.Protocol != "3" {
		t.Fatalf("first frame on ?protocol=3 = %s, want a protocol-3 hello", raw)
	}
	go s.readLoop()
	return s
}

func (s *mutevSocket) readLoop() {
	defer close(s.done)
	for {
		_, raw, err := s.conn.Read(context.Background())
		if err != nil {
			s.mu.Lock()
			s.closeErr = err
			s.mu.Unlock()
			return
		}
		var f mutevFrame
		_ = json.Unmarshal(raw, &f)
		f.Raw = raw
		s.mu.Lock()
		s.frames = append(s.frames, f)
		s.mu.Unlock()
	}
}

func (s *mutevSocket) mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

func (s *mutevSocket) snapshot() []mutevFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mutevFrame(nil), s.frames...)
}

// sub sends {"op":"sub"} and waits for the matching subscribed ack.
func (s *mutevSocket) sub(t *testing.T, ref string, resume map[string]any, topics ...map[string]any) mutevFrame {
	t.Helper()
	op := map[string]any{"op": "sub", "ref": ref, "topics": topics}
	if resume != nil {
		op["resume"] = resume
	}
	raw, err := json.Marshal(op)
	mutevMust(t, err)
	m := s.mark()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("send sub: %v", err)
	}
	return s.expect(t, m, fmt.Sprintf("subscribed{ref:%s}", ref), func(f mutevFrame) bool {
		return f.Type == "subscribed" && f.Payload["ref"] == ref
	})
}

// has reports whether a frame since mark matches within d.
func (s *mutevSocket) has(mark int, match func(mutevFrame) bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		frames := s.snapshot()
		for _, f := range frames[min(mark, len(frames)):] {
			if match(f) {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// expect waits at most 1 s for a frame since mark that matches.
func (s *mutevSocket) expect(t *testing.T, mark int, what string, match func(mutevFrame) bool) mutevFrame {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		frames := s.snapshot()
		for _, f := range frames[min(mark, len(frames)):] {
			if match(f) {
				return f
			}
		}
		if time.Now().After(deadline) {
			var got []string
			for _, f := range frames[min(mark, len(frames)):] {
				got = append(got, string(f.Raw))
			}
			s.mu.Lock()
			closeErr := s.closeErr
			s.mu.Unlock()
			t.Fatalf("%s: no matching frame within 1 s; frames since the action: %v (socket error: %v)", what, got, closeErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *mutevSocket) expectClose(t *testing.T, code websocket.StatusCode, what string) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: the socket is still open after 3 s", what)
	}
	s.mu.Lock()
	err := s.closeErr
	s.mu.Unlock()
	if got := websocket.CloseStatus(err); got != code {
		t.Fatalf("%s: CloseStatus = %d, want %d (%v)", what, got, code, err)
	}
}

// ---------------------------------------------------------------------------------------
// Matchers

func mutevNum(m map[string]any, key string) int {
	f, _ := m[key].(float64)
	return int(f)
}

func mutevJSONString(t *testing.T, raw []byte, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, raw)
	}
	v, _ := m[key].(string)
	if v == "" {
		t.Fatalf("response has no %q: %s", key, raw)
	}
	return v
}

func mutevAppUpserted(id string, extra func(map[string]any) bool) func(mutevFrame) bool {
	return func(f mutevFrame) bool {
		if f.Topic != "apps" || f.Type != "app.upserted" {
			return false
		}
		a, _ := f.Payload["app"].(map[string]any)
		if a == nil || a["id"] != id {
			return false
		}
		return extra == nil || extra(a)
	}
}

func mutevConfigChanged(id, part string) func(mutevFrame) bool {
	return func(f mutevFrame) bool {
		return f.Topic == "apps" && f.Type == "app.config_changed" && f.AppID == id && f.Payload["part"] == part
	}
}

func mutevJobEvent(typ, jobID, status string) func(mutevFrame) bool {
	return func(f mutevFrame) bool {
		return f.Topic == "jobs" && f.Type == typ && f.JobID == jobID && f.Payload["status"] == status
	}
}

func mutevJobStatus(jobID, status string) func(mutevFrame) bool {
	return mutevJobEvent("job_status", jobID, status)
}

func mutevSummary(total, enabled, running, queued int) func(mutevFrame) bool {
	return func(f mutevFrame) bool {
		return f.Topic == "summary" && f.Type == "summary" &&
			f.Payload != nil &&
			mutevNum(f.Payload, "appsTotal") == total && mutevNum(f.Payload, "appsEnabled") == enabled &&
			mutevNum(f.Payload, "running") == running && mutevNum(f.Payload, "queued") == queued
	}
}

func mutevStatsChanged(reason string) func(mutevFrame) bool {
	return func(f mutevFrame) bool {
		if f.Topic != "stats" || f.Type != "stats.changed" {
			return false
		}
		reasons, _ := f.Payload["reasons"].([]any)
		for _, r := range reasons {
			if r == reason {
				return true
			}
		}
		return false
	}
}

func mutevSection(topic, typ, section string) func(mutevFrame) bool {
	return func(f mutevFrame) bool {
		return f.Topic == topic && f.Type == typ && f.Payload["section"] == section
	}
}

func mutevServiceStatus(appID, state string) func(mutevFrame) bool {
	return func(f mutevFrame) bool {
		return f.Topic == "service" && f.Type == "service.status" && f.AppID == appID && f.Payload["state"] == state
	}
}

// mutevAckState returns topics[name].state of a subscribed ack.
func mutevAckState(ack mutevFrame, name string) map[string]any {
	topics, _ := ack.Payload["topics"].([]any)
	for _, tp := range topics {
		m, _ := tp.(map[string]any)
		if m != nil && m["name"] == name {
			st, _ := m["state"].(map[string]any)
			return st
		}
	}
	return nil
}

func mutevServiceAckHas(ack mutevFrame, appID, state string) bool {
	st := mutevAckState(ack, "service")
	statuses, _ := st["statuses"].([]any)
	for _, s := range statuses {
		m, _ := s.(map[string]any)
		if m != nil && m["appId"] == appID && m["state"] == state {
			return true
		}
	}
	return false
}

// mutevNoSecrets fails when any frame of the sockets contains one of the values (FR-005).
func mutevNoSecrets(t *testing.T, sockets []*mutevSocket, secrets map[string]string) {
	t.Helper()
	for _, s := range sockets {
		frames := append([]mutevFrame{s.Hello}, s.snapshot()...)
		for _, f := range frames {
			for what, v := range secrets {
				if v != "" && bytes.Contains(f.Raw, []byte(v)) {
					t.Errorf("FR-005: a %s frame carries the %s", f.Type, what)
				}
			}
		}
	}
}
