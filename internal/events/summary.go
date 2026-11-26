package events

import (
	"sync"
)

// SummaryState is the state of topic summary (data-model.md "SummaryState"): the payload of
// the summary event and the state of its subscribed ack.
type SummaryState struct {
	AppsTotal   int `json:"appsTotal"`
	AppsEnabled int `json:"appsEnabled"`
	Running     int `json:"running"`
	Queued      int `json:"queued"`
}

// SummarySource computes the current state. The admin hub passes the same function that
// fills the apps and active fields of GET /stats (store.DeployQueue.Summary), so the Header
// and the dashboard never disagree (FR-002).
type SummarySource func() (SummaryState, error)

// Summary keeps the last published summary and publishes a new summary event only when
// the state changed (FR-002). Refresh is called after every app mutation and job transition.
type Summary struct {
	src     SummarySource
	publish func(Event)

	// mu serializes compute+publish, so two refreshes can never publish out of order.
	mu    sync.Mutex
	last  SummaryState
	known bool
}

// NewSummary creates the tracker; nothing is computed until the first Refresh/Current.
func NewSummary(src SummarySource, publish func(Event)) *Summary {
	return &Summary{src: src, publish: publish}
}

// Refresh recomputes the state and publishes it when it differs from the last one. It
// reports whether a summary event was published. An error of the source publishes nothing.
func (s *Summary) Refresh() bool {
	_, published, _ := s.compute()
	return published
}

// Current is the state for a subscribed ack: recomputed now (and published first when it
// changed, so the ack and the event stream agree).
func (s *Summary) Current() (SummaryState, error) {
	st, _, err := s.compute()
	return st, err
}

func (s *Summary) compute() (SummaryState, bool, error) {
	if s == nil || s.src == nil {
		return SummaryState{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.src()
	if err != nil {
		return SummaryState{}, false, err
	}
	if s.known && st == s.last {
		return st, false, nil
	}
	s.last, s.known = st, true
	if s.publish != nil {
		s.publish(Event{Topic: TopicSummary, Type: TypeSummary, Payload: st})
	}
	return st, true, nil
}
