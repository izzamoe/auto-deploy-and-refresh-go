package progress

import (
	"io"
	"time"
)

const defaultCountingReaderInterval = 500 * time.Millisecond
const speedWindowSize = 5

type ProgressCallback func(downloaded, total int64, speedBPS float64)

type speedSample struct {
	bytes   int64
	elapsed time.Duration
}

type CountingReader struct {
	r          io.Reader
	totalBytes int64
	downloaded int64
	callback   ProgressCallback
	interval   time.Duration
	lastReport time.Time
	startTime  time.Time
	window     []speedSample
	now        func() time.Time
	lastSent   int64
}

func NewCountingReader(r io.Reader, totalBytes int64, cb ProgressCallback) *CountingReader {
	return newCountingReaderWithClock(r, totalBytes, cb, time.Now)
}

func newCountingReaderWithClock(r io.Reader, totalBytes int64, cb ProgressCallback, now func() time.Time) *CountingReader {
	if totalBytes <= 0 {
		totalBytes = -1
	}
	if now == nil {
		now = time.Now
	}
	started := now()
	return &CountingReader{
		r:          r,
		totalBytes: totalBytes,
		callback:   cb,
		interval:   defaultCountingReaderInterval,
		lastReport: started,
		startTime:  started,
		now:        now,
	}
}

func (cr *CountingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 {
		cr.downloaded += int64(n)
		cr.maybeReport(false)
	}
	if err == io.EOF {
		cr.maybeReport(true)
	}
	return n, err
}

func (cr *CountingReader) maybeReport(force bool) {
	if cr.callback == nil {
		return
	}
	current := cr.now()
	if !force && current.Sub(cr.lastReport) < cr.interval {
		return
	}
	elapsed := max(current.Sub(cr.lastReport), 0)
	bytesSinceLast := cr.downloaded - cr.lastSent
	if bytesSinceLast > 0 && elapsed > 0 {
		cr.window = append(cr.window, speedSample{bytes: bytesSinceLast, elapsed: elapsed})
		if len(cr.window) > speedWindowSize {
			cr.window = append([]speedSample(nil), cr.window[len(cr.window)-speedWindowSize:]...)
		}
	}
	cr.lastReport = current
	cr.lastSent = cr.downloaded
	cr.callback(cr.downloaded, cr.totalBytes, cr.rollingSpeed())
}

func (cr *CountingReader) rollingSpeed() float64 {
	var bytes int64
	var elapsed time.Duration
	for _, sample := range cr.window {
		bytes += sample.bytes
		elapsed += sample.elapsed
	}
	if bytes <= 0 || elapsed <= 0 {
		return 0
	}
	return float64(bytes) / elapsed.Seconds()
}
