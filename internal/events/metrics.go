package events

import (
	"expvar"
	"sync"
)

// Metrics are the counters of research.md section 14 (FR-025), published with expvar. The
// expvar package serves /debug/vars on http.DefaultServeMux, which this binary only serves
// on the PPROF_ADDR listener (loopback, validated by internal/config); no public endpoint.
type Metrics struct {
	Connections         *expvar.Int // events_connections: open protocol-3 sockets
	Subscriptions       *expvar.Map // events_subscriptions{topic}: current subscriptions
	FramesSent          *expvar.Map // events_frames_sent{type}
	BytesSent           *expvar.Int // events_bytes_sent
	OverflowTotal       *expvar.Int // events_overflow_total
	ResyncTotal         *expvar.Map // events_resync_total{reason}
	ReplayTotal         *expvar.Int // events_replay_total: replayed events
	OpsRejected         *expvar.Map // events_ops_rejected{reason}
	ServiceWatchRuns    *expvar.Int // service_watch_runs (T045)
	ServiceWatchMsTotal *expvar.Int // service_watch_ms_total (T045)
	LogsFollowActive    *expvar.Int // logs_follow_active (T046)
	LogsLinesDropped    *expvar.Int // logs_lines_dropped (T046)
}

// MetricNames are the published expvar names, in research.md order.
var MetricNames = []string{
	"events_connections", "events_subscriptions", "events_frames_sent", "events_bytes_sent",
	"events_overflow_total", "events_resync_total", "events_replay_total", "events_ops_rejected",
	"service_watch_runs", "service_watch_ms_total", "logs_follow_active", "logs_lines_dropped",
}

var (
	metricsOnce sync.Once
	metrics     *Metrics
)

// M returns the process metrics, publishing them on first use. expvar panics on a name
// published twice ("Reuse of exported var name"), so registration happens exactly once
// however many buses or test servers a process creates (plan.md section 7).
func M() *Metrics {
	metricsOnce.Do(func() {
		m := &Metrics{
			Connections: new(expvar.Int), Subscriptions: new(expvar.Map).Init(), FramesSent: new(expvar.Map).Init(),
			BytesSent: new(expvar.Int), OverflowTotal: new(expvar.Int), ResyncTotal: new(expvar.Map).Init(),
			ReplayTotal: new(expvar.Int), OpsRejected: new(expvar.Map).Init(), ServiceWatchRuns: new(expvar.Int),
			ServiceWatchMsTotal: new(expvar.Int), LogsFollowActive: new(expvar.Int), LogsLinesDropped: new(expvar.Int),
		}
		for name, v := range map[string]expvar.Var{
			"events_connections": m.Connections, "events_subscriptions": m.Subscriptions, "events_frames_sent": m.FramesSent,
			"events_bytes_sent": m.BytesSent, "events_overflow_total": m.OverflowTotal, "events_resync_total": m.ResyncTotal,
			"events_replay_total": m.ReplayTotal, "events_ops_rejected": m.OpsRejected, "service_watch_runs": m.ServiceWatchRuns,
			"service_watch_ms_total": m.ServiceWatchMsTotal, "logs_follow_active": m.LogsFollowActive, "logs_lines_dropped": m.LogsLinesDropped,
		} {
			expvar.Publish(name, v)
		}
		metrics = m
	})
	return metrics
}

// FrameSent counts one frame of the given type handed to a socket writer and its bytes.
func (m *Metrics) FrameSent(typ string, n int) {
	m.FramesSent.Add(typ, 1)
	m.BytesSent.Add(int64(n))
}
