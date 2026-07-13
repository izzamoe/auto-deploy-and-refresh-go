// Package telegram sends deploy notifications over Telegram using gotd/td,
// an MTProto client library (not the simple HTTP Bot API). Because gotd/td
// requires a long-lived, authenticated connection (via Client.Run, which
// blocks until the context is cancelled or the callback returns), Manager
// runs that connection in a background goroutine and relays outgoing
// messages to it over a small job queue, so callers such as an HTTP
// handler on a deploy-finished event never block on network I/O.
//
// Scope (v1): only username-based targets (an @channel or a user's
// @username) are supported. Sending to a bare numeric chat id without a
// username requires an access_hash the bot does not have unless it has
// previously received an update from that chat — a Telegram-platform
// limitation, not specific to this library. Numeric chat id / access_hash
// caching is out of scope for now.
package telegram

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
)

// jobQueueSize bounds how many pending notifications Manager buffers before
// it starts dropping them. A deploy-finished hook must never block on the
// notifier, so Notify always drops (with a log) rather than waiting for
// room in the queue.
const jobQueueSize = 16

// Reconnect backoff bounds. The MTProto connection can fail transiently at
// startup (e.g. "migrate to dc: context deadline exceeded" when the initial
// data-center migration is slow). Such a failure must not kill the notifier
// permanently — run() retries with exponential backoff between these bounds
// until it connects or the manager is stopped.
const (
	initialReconnectBackoff = 2 * time.Second
	maxReconnectBackoff     = 60 * time.Second
)

// Notifier sends a short text notification. Callers should always hold a
// non-nil Notifier (defaulting to NopNotifier) so they never need a nil
// check before calling Notify.
type Notifier interface {
	Notify(text string)
}

// NopNotifier is a no-op Notifier, used when Telegram notifications are not
// configured or not enabled.
type NopNotifier struct{}

// Notify does nothing.
func (NopNotifier) Notify(string) {}

var _ Notifier = NopNotifier{}

// messageSender is the minimal "send one message" surface Manager needs.
// It is extracted from the concrete gotd/td sender so tests can substitute
// a fake instead of driving a real MTProto connection (which requires live
// credentials and network access, and so cannot be exercised in CI).
type messageSender interface {
	Send(ctx context.Context, chatUsername, text string) error
}

// gotdSender adapts github.com/gotd/td/telegram/message.Sender to messageSender.
type gotdSender struct {
	sender *message.Sender
}

func (g *gotdSender) Send(ctx context.Context, chatUsername, text string) error {
	_, err := g.sender.Resolve(chatUsername).Text(ctx, text)
	return err
}

// Config holds the credentials and target needed to run a Manager.
type Config struct {
	// AppID and AppHash come from https://my.telegram.org/apps.
	AppID   int
	AppHash string
	// BotToken comes from @BotFather.
	BotToken string
	// ChatUsername is the @username of the channel/group/user to notify.
	ChatUsername string
	// SessionPath is where the gotd/td file session storage persists its
	// (sensitive) session data between restarts.
	SessionPath string
}

// Manager runs a long-lived gotd/td MTProto client in the background and
// relays Notify calls to it over a buffered job channel.
type Manager struct {
	cfg Config

	jobs   chan string
	done   chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewManager creates a Manager for cfg. Call Start to bring up the
// background connection.
func NewManager(cfg Config) *Manager {
	return &Manager{
		cfg:  cfg,
		jobs: make(chan string, jobQueueSize),
		done: make(chan struct{}),
	}
}

var _ Notifier = (*Manager)(nil)

// Start launches the background goroutine that authenticates as the bot and
// keeps the MTProto connection alive, delivering queued Notify messages,
// until ctx is cancelled or Stop is called.
//
// This method's real wiring (telegram.NewClient, Client.Run, Auth().Bot,
// message.NewSender against the live Telegram API) cannot be exercised by
// automated tests without live credentials and network access — see
// manager_test.go for what IS covered (Notify's drop-when-full behavior,
// NopNotifier, and the message-delivery loop against a fake sender).
func (m *Manager) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.wg.Go(func() {
		defer close(m.done)
		m.run(runCtx)
	})
}

// run keeps the MTProto connection alive, reconnecting with exponential
// backoff whenever client.Run returns an error, until ctx is cancelled
// (parent shutdown or Stop). A single transient connect/auth failure —
// notably "migrate to dc: context deadline exceeded" on a slow first
// connect — must NOT leave the notifier dead until the whole process is
// restarted, which was the prior behaviour when client.Run was called only
// once. The backoff resets to its floor after every successful connection.
func (m *Manager) run(ctx context.Context) {
	backoff := initialReconnectBackoff
	for ctx.Err() == nil {
		client := telegram.NewClient(m.cfg.AppID, m.cfg.AppHash, telegram.Options{
			SessionStorage: &session.FileStorage{Path: m.cfg.SessionPath},
		})

		err := client.Run(ctx, func(rctx context.Context) error {
			if _, err := client.Auth().Bot(rctx, m.cfg.BotToken); err != nil {
				return err
			}
			backoff = initialReconnectBackoff // connected: reset backoff
			sender := &gotdSender{sender: message.NewSender(client.API())}
			return m.loop(rctx, sender)
		})

		if ctx.Err() != nil {
			return // Stop() called or parent context cancelled: clean exit.
		}
		if err != nil {
			slog.Error("telegram: client run failed, reconnecting", "err", err, "retry_in", backoff)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxReconnectBackoff)
	}
}

// loop delivers queued messages via sender until ctx is cancelled or the
// jobs channel is closed. Extracted from Start so it can be driven directly
// in tests with a fake messageSender.
func (m *Manager) loop(ctx context.Context, sender messageSender) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case text, ok := <-m.jobs:
			if !ok {
				return nil
			}
			if err := sender.Send(ctx, m.cfg.ChatUsername, text); err != nil {
				slog.Error("telegram: send failed", "err", err)
			}
		}
	}
}

// Notify enqueues text for delivery without blocking the caller. If the
// internal queue is full, the message is dropped and logged rather than
// blocking — a deploy-finished hook must never stall on a notifier.
func (m *Manager) Notify(text string) {
	select {
	case m.jobs <- text:
	default:
		slog.Warn("telegram: notify queue full, dropping message", "text", text)
	}
}

// Stop cancels the run context and waits for the background goroutine
// launched by Start to exit. It is safe to call only after Start.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	<-m.done
	m.wg.Wait()
}
