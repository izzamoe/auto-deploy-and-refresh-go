package admin

import (
	"net/http"
	"testing"
)

// T089 CR-1 (independent code review): RED before the fix, GREEN after
// (reference/executor/T089-red.log, T089.log).

// CR-1 (HIGH): app.upserted from a mutation (toggle, edit, secret rotation) must carry the
// row of GET /apps with its lastDeploy*/lastJob* fields: a payload without them used to wipe
// "Last deploy", the last job status and the live progress of the card in every tab.
func TestT089_CR1_AppUpsertedFromMutationKeepsLastDeploy(t *testing.T) {
	e := newMutevEnv(t)
	s := e.dial(t, e.cookie)
	s.sub(t, "r1", nil, mutevTopic("apps"), mutevTopic("jobs"))
	id := e.createApp(t, mutevAppBody("cr1", "Cr1qT2xK9pL4wZ8rB3nH6jD1fG5sC0aE"))

	m := s.mark()
	e.mustDo(t, "POST", "/apps/"+id+"/deploy", e.cookie, map[string]string{"tag": "v1.0.0"}, http.StatusAccepted)
	job := e.jobID(t, id, "v1.0.0")
	s.expect(t, m, "deploy in_progress", mutevJobStatus(job, "in_progress"))
	m = s.mark()
	e.runner.finish("v1.0.0", nil)
	s.expect(t, m, "MarkDone -> app.upserted (lastDeploy*)", mutevAppUpserted(id, func(a map[string]any) bool { return a["lastJobId"] == job }))

	hasLast := func(a map[string]any) bool {
		return a["lastDeployTag"] == "v1.0.0" && a["lastJobId"] == job && a["lastJobStatus"] == "succeeded" && a["lastDeployStatus"] != nil && a["lastDeployTime"] != nil
	}
	for _, step := range []struct {
		what   string
		method string
		path   string
		body   any
		extra  func(map[string]any) bool
	}{
		{"toggle", "POST", "/apps/" + id + "/toggle", map[string]bool{"enabled": false}, func(a map[string]any) bool { return a["enabled"] == false }},
		{"edit", "PUT", "/apps/" + id, map[string]any{
			"name": "mutev-cr1-renamed", "binaryPath": "/opt/mutev-cr1/app", "serviceName": "mutev-cr1.service",
			"githubRepo": "owner/mutev-cr1", "artifactName": "mutev-cr1-linux",
		}, func(a map[string]any) bool { return a["name"] == "mutev-cr1-renamed" }},
		{"rotate", "POST", "/apps/" + id + "/webhook-secret/rotate", nil, nil},
	} {
		m = s.mark()
		e.mustDo(t, step.method, step.path, e.cookie, step.body, http.StatusOK)
		f := s.expect(t, m, step.what+" -> app.upserted", mutevAppUpserted(id, step.extra))
		a, _ := f.Payload["app"].(map[string]any)
		if !hasLast(a) {
			t.Errorf("CR-1 %s: app.upserted lost the last deploy/job fields: %s", step.what, f.Raw)
		}
		if _, ok := a["liveProgress"]; ok {
			t.Errorf("CR-1 %s: progress only travels on topic jobs: %s", step.what, f.Raw)
		}
	}
}
