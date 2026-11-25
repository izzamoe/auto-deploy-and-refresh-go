package regression

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/izzamoe/auto-deploy/internal/admin"
	"github.com/izzamoe/auto-deploy/internal/cancel"
	"github.com/izzamoe/auto-deploy/internal/deploy"
	"github.com/izzamoe/auto-deploy/internal/download"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
)

// B9 (proof P10): when "systemctl restart" fails the previous binary must be restored.
func TestRegression_B9_RestartFailureNoRollback(t *testing.T) {
	newBin := append([]byte{0x7f, 'E', 'L', 'F'}, []byte("NEW-BINARY")...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(newBin) }))
	defer srv.Close()

	dir := t.TempDir()
	bin := filepath.Join(dir, "app")
	os.WriteFile(bin, append([]byte{0x7f, 'E', 'L', 'F'}, []byte("OLD-BINARY")...), 0755)

	var calls []string
	restore := deploy.SetRunSystemctlForTest(func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name)
		if name == "restart" {
			return []byte("Job for app.service failed"), errors.New("exit status 1")
		}
		return []byte("active"), nil
	})
	defer restore()

	app := &store.App{ID: "a1", BinaryPath: bin, ServiceName: "app.service", GithubRepo: "o/r", ArtifactName: "app"}
	tr := progress.NewProgressTracker()
	_, err := deploy.DeployArtifact(app, "job1", "v1", tr, download.NewDownloadClient(""), nil, fixedResolver{srv.URL})
	cur, _ := os.ReadFile(bin)
	_, bakErr := os.Stat(bin + ".bak")
	t.Logf("err=%v systemctl calls=%v installed=%q bak_exists=%v", err, calls, cur[4:], bakErr == nil)
	if string(cur[4:]) == "NEW-BINARY" {
		t.Error("B9: restart failed but the NEW binary is left installed (no rollback, only 1 restart call)")
	}
}

// B10 (proof P4): a canceled deploy must finish the progress tracker entry in a
// terminal phase (failed; the job status itself is "canceled").
func TestRegression_B10_CanceledDeployLeavesTrackerStuck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(200)
		w.Write([]byte{0x7f, 'E', 'L', 'F'})
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()

	db := openDB(t)
	q, _ := store.NewDeployQueue(db, 10)
	q.Migrate()
	q.EnqueueManual("a1", "v1")
	jobID, _, _ := q.DequeueNext("a1")
	cs := cancel.NewCancelService(q)

	tr := progress.NewProgressTracker()
	app := &store.App{ID: "a1", BinaryPath: filepath.Join(t.TempDir(), "app"), ServiceName: "app.service"}
	go func() { time.Sleep(200 * time.Millisecond); cs.RequestJobCancel(jobID) }()
	_, err := deploy.DeployArtifact(app, jobID, "v1", tr, download.NewDownloadClient(""), cs, fixedResolver{srv.URL})
	job, _ := q.GetJob(jobID)
	tr.Cleanup()
	snap, ok := tr.Snapshot("a1")
	t.Logf("err=%v job.status=%s tracker ok=%v phase=%v", err, job.Status, ok, snap)
	if ok && snap.Phase != progress.ProgressStageFailed && snap.Phase != progress.ProgressStageSucceeded {
		t.Errorf("B10: job is %q but tracker phase is still %q (never finished => never cleaned, re-sent in every WS snapshot)", job.Status, snap.Phase)
	}
}

type sinkCap struct{ evs []cancel.CancelEvent }

func (s *sinkCap) BroadcastCancelEvent(e cancel.CancelEvent) { s.evs = append(s.evs, e) }

// B11 (proof P3): cancel/job_status events must carry jobId in payload as well
// (contract events-ws §3.5: top-level jobId is mandatory and payload.jobId is
// ALSO filled for one release).
func TestRegression_B11_CancelEventShape(t *testing.T) {
	db := openDB(t)
	q, _ := store.NewDeployQueue(db, 10)
	q.Migrate()
	q.EnqueueManual("a1", "v1")
	jobID, _, _ := q.DequeueNext("a1")
	cs := cancel.NewCancelService(q)
	s := &sinkCap{}
	cs.SetEventSink(s)
	cs.RequestJobCancel(jobID)
	if len(s.evs) == 0 {
		t.Fatal("B11: no cancel event emitted by RequestJobCancel")
	}
	for _, e := range s.evs {
		// identical mapping to AdminEventHub.BroadcastCancelEvent (admin_events.go:302-304)
		b, _ := json.Marshal(admin.AdminEvent{Protocol: "2", Type: e.Type, AppID: e.AppID, JobID: e.JobID, Payload: e.Payload})
		var m map[string]any
		json.Unmarshal(b, &m)
		p, _ := m["payload"].(map[string]any)
		t.Logf("wire event: %s", b)
		if _, has := p["jobId"]; !has {
			t.Errorf("B11: %s event has no payload.jobId; AdminEventProvider.tsx:209-229 ignores it", e.Type)
		}
	}
}

// redirectLeak drives 15 redirected downloads and fails when connections leak
// across redirects. relative=false uses an absolute Location (GitHub signed-URL
// style, B1); relative=true uses a relative Location via http.Redirect (B2).
func redirectLeak(t *testing.T, relative bool, id string) {
	var newConns atomic.Int64
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/r", func(w http.ResponseWriter, r *http.Request) {
		if relative {
			http.Redirect(w, r, "/f", http.StatusFound)
			return
		}
		w.Header().Set("Location", base+"/f") // absolute, like GitHub's signed-URL redirect
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/f", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
		if s == http.StateNew {
			newConns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	base = srv.URL

	c := download.NewDownloadClient("")
	var failures int
	for i := 0; i < 15; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		resp, err := download.DownloadWithRetryContext(ctx, c, srv.URL+"/r", nil, func(time.Duration) {})
		cancel()
		if err != nil {
			failures++
			t.Logf("iter %d err=%v", i, err)
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	t.Logf("15 redirected downloads: new TCP conns=%d failures=%d", newConns.Load(), failures)
	if !testing.Short() {
		time.Sleep(20 * time.Second)
		_, err := download.DownloadWithRetryContext(context.Background(), c, srv.URL+"/f", nil, func(time.Duration) {})
		t.Logf("after 20s idle, plain (non-redirect) download err=%v", err)
	}
	if failures > 0 || newConns.Load() > 10 {
		t.Errorf("%s: connections leak across redirects (conns=%d failures=%d, MaxConnsPerHost=10)", id, newConns.Load(), failures)
	}
}

// B1 (proof P5): intermediate redirect responses (streamed bodies) are never closed -> connection leak.
func TestRegression_B1_RedirectConnLeak(t *testing.T) {
	redirectLeak(t, false, "B1")
}

// B2: same leak with a RELATIVE Location (PROOF_REL=1 variant of the proof, as its own test).
func TestRegression_B2_RelativeRedirectNotResolved(t *testing.T) {
	redirectLeak(t, true, "B2")
}
