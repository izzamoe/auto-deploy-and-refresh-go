package download

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/izzamoe/auto-deploy/internal/progress"
	"github.com/izzamoe/auto-deploy/internal/store"
)

func downloadBinaryWithMaxBytes(url, tmpPath string, tracker *progress.ProgressTracker, appID string, client *client.Client, maxBytes int64) (store.DownloadSummary, error) {
	resp, err := DownloadWithRetryContext(context.Background(), client, url, nil)
	if err != nil {
		return store.DownloadSummary{}, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if maxBytes < 0 || resp.ContentLength > maxBytes {
		return store.DownloadSummary{}, fmt.Errorf("download too large")
	}
	f, err := os.Create(tmpPath)
	if err != nil {
		return store.DownloadSummary{}, err
	}
	reader := progress.NewCountingReader(resp.Body, resp.ContentLength, func(downloaded, total int64, speedBPS float64) {
		tracker.Update(appID, downloaded, total, speedBPS)
	})
	start := time.Now()
	written, err := io.Copy(f, io.LimitReader(reader, maxBytes+1))
	if err != nil {
		f.Close()
		os.Remove(tmpPath)
		return store.DownloadSummary{}, err
	}
	if written > maxBytes || (resp.ContentLength >= 0 && written != resp.ContentLength) {
		f.Close()
		os.Remove(tmpPath)
		return store.DownloadSummary{}, fmt.Errorf("download too large or incomplete")
	}
	if err := f.Close(); err != nil {
		return store.DownloadSummary{}, err
	}
	d := time.Since(start)
	return store.DownloadSummary{Bytes: written, DurationMs: d.Milliseconds(), SpeedBPS: float64(written) / d.Seconds()}, nil
}

func NewProgressTracker() *progress.ProgressTracker { return progress.NewProgressTracker() }
