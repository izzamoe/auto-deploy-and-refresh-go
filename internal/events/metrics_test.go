package events

import (
	"encoding/json"
	"expvar"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// T037 (FR-025): research.md section 14 metrics, published once with expvar, served only by
// the DefaultServeMux (the loopback PPROF_ADDR listener).

func TestMetrics_RegisteredOnce(t *testing.T) {
	a, b := M(), M()
	if a != b {
		t.Fatal("M() returned two different metric sets")
	}
	for _, name := range MetricNames {
		if expvar.Get(name) == nil {
			t.Errorf("expvar %q is not published", name)
		}
	}
	if len(MetricNames) != 12 {
		t.Errorf("%d metric names, want the 12 of research.md section 14", len(MetricNames))
	}
	// Many buses (one per test server) must not publish again (expvar would panic).
	for i := 0; i < 3; i++ {
		NewBus().Register(NewClient(session("u1")))
	}
}

func intVal(v *expvar.Int) int64 { return v.Value() }

func mapVal(m *expvar.Map, key string) int64 {
	if v, ok := m.Get(key).(*expvar.Int); ok {
		return v.Value()
	}
	return 0
}

func TestMetrics_CountBusActivity(t *testing.T) {
	m := M()
	conns, subsApps := intVal(m.Connections), mapVal(m.Subscriptions, "apps")
	sent, bytes := mapVal(m.FramesSent, "app.upserted"), intVal(m.BytesSent)
	replays, gaps, overflows := intVal(m.ReplayTotal), mapVal(m.ResyncTotal, ResyncGap), intVal(m.OverflowTotal)

	b := NewBus()
	c := NewClient(session("u1"))
	b.Register(c)
	if got := intVal(m.Connections) - conns; got != 1 {
		t.Fatalf("events_connections +%d after Register, want +1", got)
	}
	from := b.Seq()
	_, _ = b.Publish(Event{Topic: TopicApps, Type: "app.upserted", Payload: map[string]string{"k": "v"}})
	b.Subscribe(c, sub(TopicApps), &Resume{Epoch: b.Epoch(), Seq: from})
	if got := mapVal(m.Subscriptions, "apps") - subsApps; got != 1 {
		t.Fatalf("events_subscriptions{apps} +%d, want +1", got)
	}
	if got := intVal(m.ReplayTotal) - replays; got != 1 {
		t.Fatalf("events_replay_total +%d, want +1", got)
	}
	_, _ = b.Publish(Event{Topic: TopicApps, Type: "app.upserted", Payload: map[string]string{"k": "v"}})
	if got := mapVal(m.FramesSent, "app.upserted") - sent; got != 2 { // one replayed, one live
		t.Fatalf("events_frames_sent{app.upserted} +%d, want +2", got)
	}
	if intVal(m.BytesSent) <= bytes {
		t.Fatal("events_bytes_sent did not grow")
	}
	c2 := NewClient(session("u1"))
	b.Register(c2)
	b.Subscribe(c2, sub(TopicApps), &Resume{Epoch: b.Epoch(), Seq: b.Seq() + 1})
	if got := mapVal(m.ResyncTotal, ResyncGap) - gaps; got != 1 {
		t.Fatalf("events_resync_total{gap} +%d, want +1", got)
	}
	for i := 0; i < ClientQueueSize+1; i++ {
		_, _ = b.Publish(Event{Topic: TopicApps, Type: "app.deleted"})
	}
	c.TakeOverflow(time.Now())
	if got := intVal(m.OverflowTotal) - overflows; got < 1 {
		t.Fatalf("events_overflow_total +%d, want >= 1", got)
	}
	b.Unregister(c)
	b.Unregister(c2)
	if got := intVal(m.Connections) - conns; got != 0 {
		t.Fatalf("events_connections %+d after both sockets left, want 0", got)
	}
	if got := mapVal(m.Subscriptions, "apps") - subsApps; got != 0 {
		t.Fatalf("events_subscriptions{apps} %+d after both sockets left, want 0", got)
	}
}

// The metrics are on /debug/vars of http.DefaultServeMux only (served by the PPROF_ADDR
// listener); they are JSON with every name.
func TestMetrics_ServedByDefaultServeMux(t *testing.T) {
	M()
	rec := httptest.NewRecorder()
	http.DefaultServeMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/vars", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/debug/vars = %d", rec.Code)
	}
	var vars map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &vars); err != nil {
		t.Fatalf("/debug/vars is not JSON: %v", err)
	}
	for _, name := range MetricNames {
		if _, ok := vars[name]; !ok {
			t.Errorf("/debug/vars lacks %q", name)
		}
	}
}
