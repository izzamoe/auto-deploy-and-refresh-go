package coordinator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
	"github.com/izzamoe/auto-deploy/internal/telegram"
)

// sleepOrDone waits for d or until ctx is cancelled. It reports false when ctx
// was cancelled, so callers can return promptly during shutdown.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// DeployRunner is the legacy runner kept for backward compatibility with main.go.
type DeployRunner func(tag string) error

// Worker is the legacy single-app worker kept for backward compatibility with main.go.
type Worker struct {
	q      *store.DeployQueue
	runner DeployRunner
	appID  string
	wg     sync.WaitGroup
}

func NewWorker(q *store.DeployQueue, runner DeployRunner) *Worker {
	return &Worker{q: q, runner: runner, appID: "legacy"}
}

func (w *Worker) Start(ctx context.Context) {
	w.wg.Go(func() {
		w.loop(ctx)
	})
}

func (w *Worker) Wait() {
	w.wg.Wait()
}

func (w *Worker) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		id, tag, err := w.q.DequeueNext(w.appID)
		if err != nil {
			slog.Error("dequeue failed", "err", err)
			if !sleepOrDone(ctx, 100*time.Millisecond) {
				return
			}
			continue
		}

		if id == "" {
			if !sleepOrDone(ctx, 100*time.Millisecond) {
				return
			}
			continue
		}

		err = w.runner(tag)
		success := err == nil

		var errMsg string
		if err != nil {
			errMsg = err.Error()
		}

		if markErr := w.q.MarkDone(id, success, errMsg, nil); markErr != nil {
			slog.Error("mark done failed", "id", id, "err", markErr)
		}

		if success {
			slog.Info("deploy succeeded", "tag", tag)
		} else {
			slog.Error("deploy failed", "tag", tag, "err", err)
		}
	}
}

type CoordinatorRunner func(app *store.App, jobID, tag string) (store.DownloadSummary, error)

type Coordinator struct {
	apps        *store.AppStore
	q           *store.DeployQueue
	runner      CoordinatorRunner
	tracker     *progress.ProgressTracker
	notifier    telegram.Notifier
	logCapturer func(serviceName string, since time.Time) string
	wg          sync.WaitGroup

	mu         sync.Mutex
	activeApps map[string]bool
}

func NewCoordinator(apps *store.AppStore, q *store.DeployQueue, runner CoordinatorRunner, tracker *progress.ProgressTracker) *Coordinator {
	return &Coordinator{
		apps:       apps,
		q:          q,
		runner:     runner,
		tracker:    tracker,
		notifier:   telegram.NopNotifier{},
		activeApps: make(map[string]bool),
	}
}

// SetNotifier attaches a Telegram (or other) notifier used to announce
// deploy outcomes. It defaults to telegram.NopNotifier{} so callers of
// NewCoordinator never need a nil check. Safe to call before Start.
func (c *Coordinator) SetNotifier(n telegram.Notifier) {
	if n == nil {
		n = telegram.NopNotifier{}
	}
	c.mu.Lock()
	c.notifier = n
	c.mu.Unlock()
}

// SetLogCapturer attaches a function that returns a service's journal since a
// given time (e.g. deploy.CaptureServiceLogsSince). When set, the coordinator
// snapshots the service log after every completed deploy — scoped to that
// deploy's execution window — and stores it on the job, so a failed deploy's
// health-check logs can be reviewed later. Safe to call before Start; a nil
// capturer disables the feature.
func (c *Coordinator) SetLogCapturer(fn func(serviceName string, since time.Time) string) {
	c.mu.Lock()
	c.logCapturer = fn
	c.mu.Unlock()
}

func (c *Coordinator) Start(ctx context.Context) {
	c.wg.Go(func() {
		c.schedule(ctx)
	})

	if c.tracker != nil {
		c.wg.Go(func() {
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					c.tracker.Cleanup()
				}
			}
		})
	}
}

func (c *Coordinator) Wait() {
	c.wg.Wait()
}

func (c *Coordinator) schedule(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		apps, err := c.apps.List()
		if err != nil {
			slog.Error("coordinator: list apps failed", "err", err)
			if !sleepOrDone(ctx, 100*time.Millisecond) {
				return
			}
			continue
		}

		dispatched := false
		for i := range apps {
			app := apps[i]
			if !app.Enabled {
				continue
			}

			c.mu.Lock()
			busy := c.activeApps[app.ID]
			c.mu.Unlock()
			if busy {
				continue
			}

			id, tag, err := c.q.DequeueNext(app.ID)
			if err != nil {
				slog.Error("coordinator: dequeue failed", "app", app.ID, "err", err)
				continue
			}
			if id == "" {
				continue
			}

			c.mu.Lock()
			c.activeApps[app.ID] = true
			c.mu.Unlock()

			dispatched = true
			c.wg.Add(1)
			go c.runDeploy(app, id, tag)
		}

		if !dispatched {
			if !sleepOrDone(ctx, 100*time.Millisecond) {
				return
			}
		}
	}
}

func (c *Coordinator) runDeploy(app store.App, jobID, tag string) {
	defer c.wg.Done()
	defer func() {
		c.mu.Lock()
		delete(c.activeApps, app.ID)
		c.mu.Unlock()
	}()

	// Timestamp before the deploy runs so the job's stored log can be scoped to
	// exactly this deploy's window (download, restart, health check) rather than
	// an arbitrary tail of the service's journal.
	deployStart := time.Now()
	summary, err := c.runner(&app, jobID, tag)
	success := err == nil

	var errMsg string
	if err != nil {
		errMsg = err.Error()
	}

	var summaryPtr *store.DownloadSummary
	if summary.Bytes > 0 {
		summaryPtr = &summary
	}

	if markErr := c.q.MarkDone(jobID, success, errMsg, summaryPtr); markErr != nil {
		slog.Error("coordinator: mark done failed", "id", jobID, "err", markErr)
	}

	c.captureJobLog(app.ServiceName, jobID, deployStart)

	if success {
		slog.Info("coordinator: deploy succeeded", "app", app.ID, "tag", tag)
		c.notify(fmt.Sprintf("✅ %s: deploy of %s succeeded", app.Name, tag))
	} else {
		slog.Error("coordinator: deploy failed", "app", app.ID, "tag", tag, "err", err)
		c.notify(fmt.Sprintf("❌ %s: deploy of %s failed: %s", app.Name, tag, errMsg))
	}
}

// captureJobLog snapshots the service's logs for this deploy's window — from
// since (when the deploy started) onward, via the configured capturer — and
// stores them on the job, so a completed (especially failed) deploy's logs can
// be reviewed later. Best-effort: failures are logged only.
func (c *Coordinator) captureJobLog(serviceName, jobID string, since time.Time) {
	c.mu.Lock()
	capture := c.logCapturer
	c.mu.Unlock()
	if capture == nil {
		return
	}
	logText := capture(serviceName, since)
	if logText == "" {
		return
	}
	if err := c.q.SaveJobLog(jobID, logText); err != nil {
		slog.Error("coordinator: save job log failed", "id", jobID, "err", err)
	}
}

// notify announces a deploy outcome via the configured Notifier, if any.
func (c *Coordinator) notify(text string) {
	c.mu.Lock()
	n := c.notifier
	c.mu.Unlock()
	if n != nil {
		n.Notify(text)
	}
}
