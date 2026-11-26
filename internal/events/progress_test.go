package events

import (
	"fmt"
	"testing"
	"time"
)

// T034 (NFR-003, FR-018): compact progress is coalesced per job (latest-wins), paced to at
// most one frame per job per 500 ms per client, and not sent at all to jobs{progress:false}.

func progressFrame(job string, n int) func() []byte {
	return func() []byte { return []byte(fmt.Sprintf(`{"v":"3","t":"p","j":%q,"n":%d}`, job, n)) }
}

func jobsSub(progress bool) []TopicSub {
	return []TopicSub{{Topic: TopicJobs, Opts: TopicOpts{Progress: progress}}}
}

func TestProgress_SlowClientGetsLatestOnly(t *testing.T) {
	b := NewBus()
	c := NewClient(session("u1"))
	b.Register(c)
	b.Subscribe(c, jobsSub(true), nil)

	for i := 1; i <= 100; i++ {
		b.PublishProgress("j1", progressFrame("j1", i))
		if i <= 50 {
			b.PublishProgress("j2", progressFrame("j2", i))
		}
	}
	if c.Overflowed() || len(c.queue) != 0 {
		t.Fatalf("progress went through the state queue (%d queued, overflow %v)", len(c.queue), c.Overflowed())
	}
	got, wait := c.TakeProgress(time.Now(), nil)
	want := []string{`{"v":"3","t":"p","j":"j1","n":100}`, `{"v":"3","t":"p","j":"j2","n":50}`}
	if len(got) != 2 || string(got[0]) != want[0] || string(got[1]) != want[1] || wait != 0 {
		t.Fatalf("a client that read nothing gets %q (wait %v), want only the newest frame per job %q", got, wait, want)
	}
	if again, _ := c.TakeProgress(time.Now(), nil); len(again) != 0 {
		t.Fatalf("taken frames came back: %q", again)
	}
}

func TestProgress_PacedPerJob(t *testing.T) {
	b := NewBus()
	c := NewClient(session("u1"))
	b.Register(c)
	b.Subscribe(c, jobsSub(true), nil)
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

	b.PublishProgress("j1", progressFrame("j1", 1))
	if got, _ := c.TakeProgress(t0, nil); len(got) != 1 {
		t.Fatalf("first frame of a job: %d sent, want 1", len(got))
	}
	b.PublishProgress("j1", progressFrame("j1", 2))
	b.PublishProgress("j1", progressFrame("j1", 3))
	b.PublishProgress("j2", progressFrame("j2", 1)) // another job is not held back
	got, wait := c.TakeProgress(t0.Add(100*time.Millisecond), nil)
	if len(got) != 1 || string(got[0]) != `{"v":"3","t":"p","j":"j2","n":1}` {
		t.Fatalf("100 ms later: sent %q, want only j2 (j1 waits)", got)
	}
	if wait != 400*time.Millisecond {
		t.Fatalf("wait = %v, want 400 ms until j1 may send again", wait)
	}
	got, _ = c.TakeProgress(t0.Add(500*time.Millisecond), nil)
	if len(got) != 1 || string(got[0]) != `{"v":"3","t":"p","j":"j1","n":3}` {
		t.Fatalf("at 500 ms: sent %q, want the newest j1 frame (n=3)", got)
	}

	// Rate over a burst: 1 frame per job per 500 ms whatever the source rate.
	sent := 0
	for ms := 0; ms <= 2000; ms += 10 {
		b.PublishProgress("j3", progressFrame("j3", ms))
		out, _ := c.TakeProgress(t0.Add(time.Second+time.Duration(ms)*time.Millisecond), nil)
		for range out {
			sent++
		}
	}
	if sent > 5 { // j3 (and nothing else) over 2 s: at 0, 500, 1000, 1500, 2000 ms
		t.Fatalf("a 100/s source gave %d frames in 2 s, want <= 5 (<= 2 frames/s/job, NFR-003)", sent)
	}
}

// FR-018: a hidden tab subscribes jobs{progress:false}: no progress frame, state events still
// flow; jobs{progress:true} again turns the stream back on (the hub then sends a snapshot).
func TestProgress_HiddenOptOut(t *testing.T) {
	b := NewBus()
	c := NewClient(session("u1"))
	b.Register(c)
	b.Subscribe(c, jobsSub(true), nil)
	b.PublishProgress("j1", progressFrame("j1", 1)) // pending when the tab hides

	b.Subscribe(c, jobsSub(false), nil)
	if got, _ := c.TakeProgress(time.Now(), nil); len(got) != 0 {
		t.Fatalf("switching to progress:false must drop pending frames, got %q", got)
	}
	encoded := false
	b.PublishProgress("j1", func() []byte { encoded = true; return []byte("x") })
	if encoded {
		t.Error("a frame was encoded although no client wants progress")
	}
	if got, _ := c.TakeProgress(time.Now(), nil); len(got) != 0 {
		t.Fatalf("progress:false client got %q", got)
	}
	if _, err := b.Publish(Event{Topic: TopicJobs, Type: "job_status", JobID: "j1", Payload: map[string]string{"status": "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if q := drainQueue(c); len(q) != 1 {
		t.Fatalf("job_status with progress:false: %d frames, want 1 (state events still flow)", len(q))
	}

	b.Subscribe(c, jobsSub(true), nil)
	b.PublishProgress("j1", progressFrame("j1", 2))
	if got, _ := c.TakeProgress(time.Now(), nil); len(got) != 1 {
		t.Fatalf("progress:true again: %d frames, want 1", len(got))
	}

	// Leaving jobs drops pending frames and stops the stream.
	b.PublishProgress("j1", progressFrame("j1", 3))
	b.Unsubscribe(c, []Topic{TopicJobs})
	b.PublishProgress("j1", progressFrame("j1", 4))
	if got, _ := c.TakeProgress(time.Now().Add(time.Second), nil); len(got) != 0 {
		t.Fatalf("after unsub jobs: %q", got)
	}
}

// Other topics never receive progress.
func TestProgress_OnlyJobsSubscribers(t *testing.T) {
	b := NewBus()
	acct := NewClient(session("u1"))
	b.Register(acct)
	b.Subscribe(acct, sub(TopicAccount, TopicSummary, TopicApps), nil)
	b.PublishProgress("j1", progressFrame("j1", 1))
	if got, _ := acct.TakeProgress(time.Now(), nil); len(got) != 0 {
		t.Fatalf("SC-011: a client without jobs got %q", got)
	}
	select {
	case <-acct.Wake():
		t.Fatal("a client without jobs was woken by progress")
	default:
	}
}
