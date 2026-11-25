package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/izzamoe/auto-deploy/internal/github"
)

// notYetCovered lists IMPLEMENTED operations that have no contract case yet, with the
// reason. They touch systemd/journald/GitHub, which need fakes first. The list must
// be empty by phase 7 (T106); an entry that gains a case fails the test so the list
// shrinks, never rots.
var notYetCovered = map[string]string{
	"applyServiceUnit": "writes /etc/systemd/system and runs systemctl; needs a fake unit dir/systemctl (Fase 3, B7/B8 tasks)",
	"getAppLogs":       "runs journalctl; needs a fake journal reader (Fase 3)",
	"getServiceStatus": "runs systemctl is-active; needs a fake systemctl (Fase 3)",
	"controlService":   "runs systemctl; needs a fake systemctl (Fase 3)",
	"getSystemLogs":    "runs journalctl for the auto-deploy unit; needs a fake journal reader (Fase 3)",
}

func TestContract(t *testing.T) {
	cov := newContractCoverage()
	e := newContractEnv(t, cov)

	t.Run("session", func(t *testing.T) {
		e.do(contractCall{op: "getSession", method: "GET", path: "/admin/api/v1/session", status: 200})
		e.do(contractCall{op: "getSession", method: "GET", path: "/admin/api/v1/session", noCookie: true, status: 401})

		login := map[string]string{"X-Admin-Request": "true", "Sec-Fetch-Site": "same-origin"}
		e.do(contractCall{op: "createSession", method: "POST", path: "/admin/api/v1/session", noCookie: true, headers: login,
			body: map[string]string{"username": "admin", "password": sessionTestPassword}, status: 200})
		e.do(contractCall{op: "createSession", method: "POST", path: "/admin/api/v1/session", noCookie: true, headers: login,
			body: map[string]string{"username": "admin", "password": "wrong"}, status: 401})
		e.do(contractCall{op: "createSession", method: "POST", path: "/admin/api/v1/session", noCookie: true,
			body: map[string]string{"username": "admin", "password": "x"}, status: 403}) // no X-Admin-Request
		e.do(contractCall{op: "createSession", method: "POST", path: "/admin/api/v1/session", noCookie: true, headers: login,
			body: `{broken`, status: 400, invalid: true})
	})

	t.Run("account", func(t *testing.T) {
		e.do(contractCall{op: "getAccount", method: "GET", path: "/admin/api/v1/account", status: 200})
		e.do(contractCall{op: "getAccount", method: "GET", path: "/admin/api/v1/account", noCookie: true, status: 401})
		e.do(contractCall{op: "changePassword", method: "POST", path: "/admin/api/v1/account/password",
			body: map[string]string{"currentPassword": "wrong", "newPassword": "a-new-password"}, status: 401})
		e.do(contractCall{op: "changePassword", method: "POST", path: "/admin/api/v1/account/password",
			body: map[string]string{"currentPassword": sessionTestPassword, "newPassword": "x"}, status: 400})
		e.do(contractCall{op: "changeUsername", method: "POST", path: "/admin/api/v1/account/username",
			body: map[string]string{"currentPassword": "wrong", "newUsername": "boss"}, status: 401})
		e.do(contractCall{op: "changeUsername", method: "POST", path: "/admin/api/v1/account/username",
			body: map[string]string{"currentPassword": sessionTestPassword, "newUsername": ""}, status: 400, invalid: true})
		e.do(contractCall{op: "changePassword", method: "POST", path: "/admin/api/v1/account/password",
			noCookie: true, body: map[string]string{"currentPassword": "a", "newPassword": "b"}, status: 401})
	})

	t.Run("tokens", func(t *testing.T) {
		e.do(contractCall{op: "listApiTokens", method: "GET", path: "/admin/api/v1/tokens", status: 200})
		e.do(contractCall{op: "listApiTokens", method: "GET", path: "/admin/api/v1/tokens", noCookie: true, status: 401})
		_, body := e.do(contractCall{op: "createApiToken", method: "POST", path: "/admin/api/v1/tokens",
			body: map[string]string{"name": "ci"}, status: 201})
		id := jsonField(t, body, "id")
		e.do(contractCall{op: "createApiToken", method: "POST", path: "/admin/api/v1/tokens",
			body: map[string]string{"name": ""}, status: 400, invalid: true})
		e.do(contractCall{op: "createApiToken", method: "POST", path: "/admin/api/v1/tokens",
			body: map[string]string{"name": "no-csrf"}, headers: map[string]string{"X-Admin-Request": "false"}, status: 403, invalid: true})
		e.do(contractCall{op: "revokeApiToken", method: "DELETE", path: "/admin/api/v1/tokens/" + id, status: 204})
		e.do(contractCall{op: "revokeApiToken", method: "DELETE", path: "/admin/api/v1/tokens/nope", status: 404})
	})

	var appID string
	t.Run("apps", func(t *testing.T) {
		e.do(contractCall{op: "listApps", method: "GET", path: "/admin/api/v1/apps", status: 200})
		e.do(contractCall{op: "listApps", method: "GET", path: "/admin/api/v1/apps", noCookie: true, status: 401})

		e.do(contractCall{op: "getStats", method: "GET", path: "/admin/api/v1/stats", status: 200})
		e.do(contractCall{op: "getStats", method: "GET", path: "/admin/api/v1/stats?range=30d&tz=Asia/Jakarta", status: 200})
		e.do(contractCall{op: "getStats", method: "GET", path: "/admin/api/v1/stats?range=14d", status: 400, invalid: true})
		e.do(contractCall{op: "getStats", method: "GET", path: "/admin/api/v1/stats", noCookie: true, status: 401})

		create := map[string]any{
			"name": "contract-app", "binaryPath": "/opt/contract/app", "serviceName": "contract.service",
			"githubRepo": "owner/contract", "artifactName": "contract-linux",
			"webhookSecret": strings.Repeat("s", 32),
			"envVars":       []map[string]string{{"name": "PORT", "value": "8080"}},
			"args":          []string{"--port", "8080"},
		}
		_, body := e.do(contractCall{op: "createApp", method: "POST", path: "/admin/api/v1/apps", body: create, status: 201})
		appID = jsonField(t, body, "app", "id")
		e.do(contractCall{op: "createApp", method: "POST", path: "/admin/api/v1/apps", body: map[string]any{"name": "x"}, status: 400, invalid: true})
		e.do(contractCall{op: "createApp", method: "POST", path: "/admin/api/v1/apps", body: `{`, status: 400, invalid: true})
		e.do(contractCall{op: "createApp", method: "POST", path: "/admin/api/v1/apps", body: create, status: 409}) // duplicate secret/path/service

		e.do(contractCall{op: "getApp", method: "GET", path: "/admin/api/v1/apps/" + appID, status: 200})
		e.do(contractCall{op: "getApp", method: "GET", path: "/admin/api/v1/apps/nope", status: 404})

		update := map[string]any{
			"name": "contract-app-2", "binaryPath": "/opt/contract/app", "serviceName": "contract.service",
			"githubRepo": "owner/contract", "artifactName": "contract-linux",
		}
		e.do(contractCall{op: "updateApp", method: "PUT", path: "/admin/api/v1/apps/" + appID, body: update, status: 200})
		e.do(contractCall{op: "updateApp", method: "PUT", path: "/admin/api/v1/apps/nope", body: update, status: 404})
		e.do(contractCall{op: "updateApp", method: "PUT", path: "/admin/api/v1/apps/" + appID, body: map[string]any{"name": ""}, status: 400, invalid: true})

		e.do(contractCall{op: "toggleApp", method: "POST", path: "/admin/api/v1/apps/" + appID + "/toggle", body: map[string]bool{"enabled": true}, status: 200})
		e.do(contractCall{op: "toggleApp", method: "POST", path: "/admin/api/v1/apps/nope/toggle", body: map[string]bool{"enabled": true}, status: 404})

		e.do(contractCall{op: "rotateWebhookSecret", method: "POST", path: "/admin/api/v1/apps/" + appID + "/webhook-secret/rotate", status: 200})
		e.do(contractCall{op: "rotateWebhookSecret", method: "POST", path: "/admin/api/v1/apps/nope/webhook-secret/rotate", status: 404})

		e.do(contractCall{op: "getAppEnv", method: "GET", path: "/admin/api/v1/apps/" + appID + "/env", status: 200})
		e.do(contractCall{op: "getAppEnv", method: "GET", path: "/admin/api/v1/apps/nope/env", status: 404})
		e.do(contractCall{op: "saveAppEnv", method: "PUT", path: "/admin/api/v1/apps/" + appID + "/env",
			body: map[string]any{"envVars": []map[string]string{{"name": "MODE", "value": "prod"}}}, status: 200})
		e.do(contractCall{op: "saveAppEnv", method: "PUT", path: "/admin/api/v1/apps/" + appID + "/env",
			body: map[string]any{"envVars": []map[string]string{{"name": "1BAD", "value": "x"}}}, status: 400, invalid: true})
		e.do(contractCall{op: "saveAppEnv", method: "PUT", path: "/admin/api/v1/apps/nope/env", body: map[string]any{"envVars": []any{}}, status: 404})

		e.do(contractCall{op: "getAppArgs", method: "GET", path: "/admin/api/v1/apps/" + appID + "/args", status: 200})
		e.do(contractCall{op: "saveAppArgs", method: "PUT", path: "/admin/api/v1/apps/" + appID + "/args", body: map[string]any{"args": []string{"--verbose"}}, status: 200})
		e.do(contractCall{op: "saveAppArgs", method: "PUT", path: "/admin/api/v1/apps/" + appID + "/args", body: map[string]any{"args": []string{"bad\narg"}}, status: 400})
		e.do(contractCall{op: "saveAppArgs", method: "PUT", path: "/admin/api/v1/apps/nope/args", body: map[string]any{"args": []string{}}, status: 404})

		e.do(contractCall{op: "previewServiceUnit", method: "GET", path: "/admin/api/v1/apps/" + appID + "/service-unit/preview", status: 200})
		e.do(contractCall{op: "previewServiceUnit", method: "GET", path: "/admin/api/v1/apps/nope/service-unit/preview", status: 404})
	})

	var jobID string
	t.Run("deploy-history-jobs", func(t *testing.T) {
		_, body := e.do(contractCall{op: "deployApp", method: "POST", path: "/admin/api/v1/apps/" + appID + "/deploy", body: map[string]string{"tag": "v1.0.0"}, status: 202})
		_ = body
		e.do(contractCall{op: "deployApp", method: "POST", path: "/admin/api/v1/apps/" + appID + "/deploy", body: map[string]string{"tag": "v1.0.0"}, status: 409})
		e.do(contractCall{op: "deployApp", method: "POST", path: "/admin/api/v1/apps/nope/deploy", body: map[string]string{"tag": "v1"}, status: 404})
		e.do(contractCall{op: "deployApp", method: "POST", path: "/admin/api/v1/apps/" + appID + "/deploy", body: map[string]string{"tag": ""}, status: 400, invalid: true})
		e.do(contractCall{op: "deployApp", method: "POST", path: "/admin/api/v1/apps/" + appID + "/deploy", body: map[string]string{"tag": "v1 bad"}, status: 400}) // invalid_tag (B15)

		_, hist := e.do(contractCall{op: "listHistory", method: "GET", path: "/admin/api/v1/history?appId=" + appID, status: 200})
		jobID = firstHistoryJobID(t, hist)
		e.do(contractCall{op: "listHistory", method: "GET", path: "/admin/api/v1/history", status: 200})
		e.do(contractCall{op: "listHistory", method: "GET", path: "/admin/api/v1/history?appId=nope", status: 404})

		e.do(contractCall{op: "getJobLogs", method: "GET", path: "/admin/api/v1/jobs/" + jobID + "/logs", status: 200})
		e.do(contractCall{op: "retryJob", method: "POST", path: "/admin/api/v1/history/" + jobID + "/retry", status: 409}) // same tag already pending
		e.do(contractCall{op: "retryJob", method: "POST", path: "/admin/api/v1/history/nope/retry", status: 404})
		e.do(contractCall{op: "cancelJob", method: "POST", path: "/admin/api/v1/jobs/" + jobID + "/cancel", status: 200})
		e.do(contractCall{op: "cancelAppJobs", method: "POST", path: "/admin/api/v1/apps/" + appID + "/cancel", status: 200})
		e.do(contractCall{op: "cancelAppJobs", method: "POST", path: "/admin/api/v1/apps/nope/cancel", status: 404})
	})

	t.Run("releases", func(t *testing.T) {
		_, body := e.do(contractCall{op: "createApp", method: "POST", path: "/admin/api/v1/apps", status: 201, body: map[string]any{
			"name": "rel-app", "binaryPath": "/opt/rel/app", "serviceName": "rel.service",
			"githubRepo": "owner/rel", "artifactName": "rel-linux", "webhookSecret": strings.Repeat("r", 32),
		}})
		relID := jsonField(t, body, "app", "id")
		e.lister.releases = []github.Release{
			{TagName: "v1.0.0", Assets: []string{"rel-linux"}},
			{TagName: "v0.9.0", Assets: []string{"other-asset"}},
		}
		e.do(contractCall{op: "listAppReleases", method: "GET", path: "/admin/api/v1/apps/" + relID + "/releases", status: 200})
		e.do(contractCall{op: "listAppReleases", method: "GET", path: "/admin/api/v1/apps/nope/releases", status: 404})
		e.lister.err = &github.StatusError{StatusCode: 404, Op: "list releases"}
		e.do(contractCall{op: "listAppReleases", method: "GET", path: "/admin/api/v1/apps/" + relID + "/releases", status: 502})
		e.do(contractCall{op: "listAppReleases", method: "GET", path: "/admin/api/v1/apps/" + relID + "/releases", noCookie: true, status: 401})
		e.lister.err = nil
	})

	t.Run("settings", func(t *testing.T) {
		e.do(contractCall{op: "getGitHubConfig", method: "GET", path: "/admin/api/v1/github", status: 200})
		e.do(contractCall{op: "saveGitHubConfig", method: "POST", path: "/admin/api/v1/github", body: map[string]any{"token": "ghp_exampleexampleexample"}, status: 200})
		e.do(contractCall{op: "getTelegramConfig", method: "GET", path: "/admin/api/v1/telegram", status: 200})
		e.do(contractCall{op: "saveTelegramConfig", method: "POST", path: "/admin/api/v1/telegram", body: map[string]any{"enabled": false}, status: 200})
	})

	t.Run("delete-app-conflict", func(t *testing.T) {
		// HEAD refuses to delete an app that has an in-progress deploy with 409; the
		// contract's deleteApp lists only 200/401/403/404 (see knownDeviations).
		_, body := e.do(contractCall{op: "createApp", method: "POST", path: "/admin/api/v1/apps", status: 201, body: map[string]any{
			"name": "busy-app", "binaryPath": "/opt/busy/app", "serviceName": "busy.service",
			"githubRepo": "owner/busy", "artifactName": "busy-linux", "webhookSecret": strings.Repeat("b", 32),
		}})
		busyID := jsonField(t, body, "app", "id")
		e.do(contractCall{op: "deployApp", method: "POST", path: "/admin/api/v1/apps/" + busyID + "/deploy", body: map[string]string{"tag": "v2.0.0"}, status: 202})
		dequeueJob(t, e.queue, busyID, "v2.0.0") // the worker picked it up: the job is now in progress
		e.do(contractCall{op: "deleteApp", method: "DELETE", path: "/admin/api/v1/apps/" + busyID, status: 409})
	})

	t.Run("delete-app", func(t *testing.T) {
		e.do(contractCall{op: "deleteApp", method: "DELETE", path: "/admin/api/v1/apps/" + appID, status: 200})
		e.do(contractCall{op: "deleteApp", method: "DELETE", path: "/admin/api/v1/apps/nope", status: 404})
	})

	// These change or end the session itself, so each gets a fresh environment.
	t.Run("session-changing", func(t *testing.T) {
		fresh := newContractEnv(t, cov)
		fresh.do(contractCall{op: "revokeAllSessions", method: "POST", path: "/admin/api/v1/account/sessions/revoke-all", status: 200})
		fresh.do(contractCall{op: "revokeAllSessions", method: "POST", path: "/admin/api/v1/account/sessions/revoke-all", noCookie: true, status: 401})

		pw := newContractEnv(t, cov)
		pw.do(contractCall{op: "changePassword", method: "POST", path: "/admin/api/v1/account/password",
			body: map[string]string{"currentPassword": sessionTestPassword, "newPassword": "a-new-password"}, status: 200})
		un := newContractEnv(t, cov)
		un.do(contractCall{op: "changeUsername", method: "POST", path: "/admin/api/v1/account/username",
			body: map[string]string{"currentPassword": sessionTestPassword, "newUsername": "boss"}, status: 200})
		if _, err := un.users.CreateUser("taken", "another-password", false); err != nil {
			t.Fatal(err)
		}
		un2 := newContractEnv(t, cov)
		if _, err := un2.users.CreateUser("taken", "another-password", false); err != nil {
			t.Fatal(err)
		}
		un2.do(contractCall{op: "changeUsername", method: "POST", path: "/admin/api/v1/account/username",
			body: map[string]string{"currentPassword": sessionTestPassword, "newUsername": "taken"}, status: 409})

		out := newContractEnv(t, cov)
		out.do(contractCall{op: "deleteSession", method: "DELETE", path: "/admin/api/v1/session", status: 204})
		out.do(contractCall{op: "deleteSession", method: "DELETE", path: "/admin/api/v1/session", noCookie: true, status: 401})
	})

	t.Run("coverage", func(t *testing.T) {
		impl := e.implementedOperations()
		var uncovered []string
		for _, id := range impl {
			if cov.covered[id] {
				if reason, ok := notYetCovered[id]; ok {
					t.Errorf("%s now has a contract case; remove it from notYetCovered (%s)", id, reason)
				}
				continue
			}
			if _, ok := notYetCovered[id]; !ok {
				uncovered = append(uncovered, id)
			}
		}
		if len(uncovered) > 0 {
			t.Errorf("implemented operations without a contract case (add a case, or list them with a reason in notYetCovered): %v", uncovered)
		}

		// Operations of the contract that the server does not serve yet: informational,
		// each is built by a later task and gains its case there.
		have := map[string]bool{}
		for _, id := range impl {
			have[id] = true
		}
		var notImplemented []string
		for id := range operationsOf(e.doc) {
			if !have[id] {
				notImplemented = append(notImplemented, id)
			}
		}
		sort.Strings(notImplemented)
		t.Logf("implemented operations (%d): %v", len(impl), impl)
		t.Logf("contract operations not served yet (%d): %v", len(notImplemented), notImplemented)
		var pending []string
		for id := range notYetCovered {
			pending = append(pending, id)
		}
		sort.Strings(pending)
		t.Logf("implemented but not yet covered (%d, must be empty by T106): %v", len(pending), pending)

		for key := range knownDeviations {
			if !cov.seenDev[key] {
				t.Errorf("known deviation %q was never observed; delete it", key)
			}
		}
	})
}

func firstHistoryJobID(t *testing.T, raw []byte) string {
	t.Helper()
	var page struct {
		History []struct {
			ID string `json:"id"`
		} `json:"history"`
	}
	if err := json.Unmarshal(raw, &page); err != nil || len(page.History) == 0 {
		t.Fatalf("history has no job: %v %s", err, raw)
	}
	return page.History[0].ID
}

var _ = http.StatusOK
