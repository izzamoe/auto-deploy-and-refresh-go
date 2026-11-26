package events

import "time"

// ProgressInterval paces compact progress frames: at most one frame per job per client per
// interval (NFR-003: <= 2 frames/s/job after coalescing). A frame that arrives earlier waits
// in the job's slot and is replaced by newer ones (latest-wins, research.md 6.4).
const ProgressInterval = 500 * time.Millisecond

// progressSlots is the per-client coalescing state (data-model.md "Client.progress").
type progressSlots struct {
	frames map[string][]byte    // jobId -> newest frame not yet sent
	order  []string             // dirty jobs, in the order they first became dirty
	last   map[string]time.Time // jobId -> when its last frame was handed to the writer
}

// setProgress puts the newest frame of a job in its slot and wakes the writer. Progress
// never goes through the state queue, so it can never cause an overflow.
func (c *Client) setProgress(jobID string, frame []byte) {
	c.pmu.Lock()
	if c.progress.frames == nil {
		c.progress.frames = make(map[string][]byte)
		c.progress.last = make(map[string]time.Time)
	}
	if _, dirty := c.progress.frames[jobID]; !dirty {
		c.progress.order = append(c.progress.order, jobID)
	}
	c.progress.frames[jobID] = frame
	c.pmu.Unlock()
	c.signal()
}

// clearProgress drops frames not yet sent (the client opted out of progress or left jobs).
func (c *Client) clearProgress() {
	c.pmu.Lock()
	c.progress = progressSlots{}
	c.pmu.Unlock()
}

// TakeProgress hands the writer, in dirty order, the newest frame of every job whose last
// frame went out at least ProgressInterval ago; wait > 0 is when the next held frame is due.
func (c *Client) TakeProgress(now time.Time, out [][]byte) (frames [][]byte, wait time.Duration) {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	kept := c.progress.order[:0]
	for _, id := range c.progress.order {
		if last, ok := c.progress.last[id]; ok {
			if due := last.Add(ProgressInterval); now.Before(due) {
				kept = append(kept, id)
				if d := due.Sub(now); wait == 0 || d < wait {
					wait = d
				}
				continue
			}
		}
		out = append(out, c.progress.frames[id])
		delete(c.progress.frames, id)
		c.progress.last[id] = now
	}
	c.progress.order = kept
	for id, last := range c.progress.last { // forget jobs that went quiet
		if _, dirty := c.progress.frames[id]; !dirty && now.Sub(last) >= ProgressInterval {
			delete(c.progress.last, id)
		}
	}
	return out, wait
}
