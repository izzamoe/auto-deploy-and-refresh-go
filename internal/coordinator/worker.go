package coordinator

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
)

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
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.loop(ctx)
	}()
}

func (w *Worker) Wait() {
	w.wg.Wait()
}

func (w *Worker) loop(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		id, tag, err := w.q.DequeueNext(w.appID)
		if err != nil {
			slog.Error("dequeue failed", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		if id == "" {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				continue
			}
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
	apps    *store.AppStore
	q       *store.DeployQueue
	runner  CoordinatorRunner
	tracker *progress.ProgressTracker
	wg      sync.WaitGroup

	mu         sync.Mutex
	activeApps map[string]bool
}

func NewCoordinator(apps *store.AppStore, q *store.DeployQueue, runner CoordinatorRunner, tracker *progress.ProgressTracker) *Coordinator {
	return &Coordinator{
		apps:       apps,
		q:          q,
		runner:     runner,
		tracker:    tracker,
		activeApps: make(map[string]bool),
	}
}

func (c *Coordinator) Start(ctx context.Context) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.schedule(ctx)
	}()

	if c.tracker != nil {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
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
		}()
	}
}

func (c *Coordinator) Wait() {
	c.wg.Wait()
}

func (c *Coordinator) schedule(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		apps, err := c.apps.List()
		if err != nil {
			slog.Error("coordinator: list apps failed", "err", err)
			time.Sleep(100 * time.Millisecond)
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
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
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

	if success {
		slog.Info("coordinator: deploy succeeded", "app", app.ID, "tag", tag)
	} else {
		slog.Error("coordinator: deploy failed", "app", app.ID, "tag", tag, "err", err)
	}
}
