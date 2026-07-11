package telegram

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeSender records every Send call instead of talking to real Telegram
// servers. Manager's real gotd/td wiring (telegram.NewClient, Client.Run,
// Auth().Bot, message.NewSender) is NOT exercised by these tests — that
// requires live app credentials, a bot token, and network access to
// Telegram's servers, none of which are available in CI. What IS covered
// here: Notify's non-blocking drop-when-full behavior, NopNotifier, and the
// message-delivery loop (Manager.loop) against this fake sender.
type fakeSender struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeSender) Send(_ context.Context, chatUsername, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, chatUsername+":"+text)
	return f.err
}

func (f *fakeSender) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSender) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func TestNopNotifierDoesNothing(t *testing.T) {
	t.Parallel()
	var n Notifier = NopNotifier{}
	// Must not panic and must not block.
	n.Notify("hello")
}

func TestManagerNotifyEnqueues(t *testing.T) {
	t.Parallel()
	m := NewManager(Config{ChatUsername: "@ops"})

	m.Notify("deploy ok")

	select {
	case got := <-m.jobs:
		if got != "deploy ok" {
			t.Fatalf("jobs received %q, want %q", got, "deploy ok")
		}
	case <-time.After(time.Second):
		t.Fatal("Notify did not enqueue a job")
	}
}

func TestManagerNotifyDropsWhenQueueFull(t *testing.T) {
	t.Parallel()
	m := NewManager(Config{})

	// Fill the queue directly (bypassing Notify, since nothing is draining it).
	for i := range jobQueueSize {
		m.jobs <- fmt.Sprintf("msg-%d", i)
	}

	done := make(chan struct{})
	go func() {
		m.Notify("overflow") // must not block even though the queue is full
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Notify blocked when the queue was full; it must drop-and-log instead")
	}

	if got := len(m.jobs); got != jobQueueSize {
		t.Fatalf("queue length = %d, want %d (overflow message should have been dropped)", got, jobQueueSize)
	}
}

func TestManagerLoopDeliversQueuedMessages(t *testing.T) {
	t.Parallel()
	m := NewManager(Config{ChatUsername: "@ops"})
	sender := &fakeSender{}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	loopDone := make(chan error, 1)
	go func() {
		loopDone <- m.loop(ctx, sender)
	}()

	m.jobs <- "deploy succeeded: v1.2.3"

	deadline := time.Now().Add(2 * time.Second)
	for sender.callCount() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for loop to deliver the queued message")
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(m.jobs)

	select {
	case err := <-loopDone:
		if err != nil {
			t.Fatalf("loop returned error after jobs channel closed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit after jobs channel closed")
	}

	calls := sender.snapshot()
	if len(calls) != 1 || calls[0] != "@ops:deploy succeeded: v1.2.3" {
		t.Fatalf("unexpected sender calls: %v", calls)
	}
}

func TestManagerLoopExitsOnContextCancel(t *testing.T) {
	t.Parallel()
	m := NewManager(Config{})
	sender := &fakeSender{}

	ctx, cancel := context.WithCancel(t.Context())

	loopDone := make(chan error, 1)
	go func() {
		loopDone <- m.loop(ctx, sender)
	}()

	cancel()

	select {
	case err := <-loopDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("loop error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit after context was cancelled")
	}
}

func TestManagerLoopLogsAndContinuesOnSendError(t *testing.T) {
	t.Parallel()
	m := NewManager(Config{ChatUsername: "@ops"})
	sender := &fakeSender{err: errors.New("boom")}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	loopDone := make(chan error, 1)
	go func() {
		loopDone <- m.loop(ctx, sender)
	}()

	m.jobs <- "first"
	m.jobs <- "second"

	deadline := time.Now().Add(2 * time.Second)
	for sender.callCount() != 2 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for loop to process both messages despite send errors")
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(m.jobs)
	select {
	case err := <-loopDone:
		if err != nil {
			t.Fatalf("loop returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit after jobs channel closed")
	}
}
