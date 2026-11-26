package events

import (
	"sort"
	"time"
)

// ServiceWatcher is what topic service needs from the shared systemd watcher
// (internal/deploy.ServiceWatcher, Q-3=A): a reference per subscribed socket, an immediate
// round after a panel action, and the last observed state for the subscribed ack.
type ServiceWatcher interface {
	Acquire()
	Release()
	Refresh(appID string)
	Snapshot() map[string]string
}

// ServiceStatusEntry is one app of the service ack state.
type ServiceStatusEntry struct {
	AppID string `json:"appId"`
	State string `json:"state"`
}

// ServiceState is the state of topic service in a subscribed ack (events-ws.v3.md
// section 3): every app's last observed state and when the watcher observed it.
type ServiceState struct {
	Statuses   []ServiceStatusEntry `json:"statuses"`
	ObservedAt time.Time            `json:"observedAt"`
}

// serviceStates is the ServiceState enum of events-ws.v3.md 2.2.
var serviceStates = map[string]bool{
	"active": true, "inactive": true, "failed": true, "activating": true,
	"deactivating": true, "reloading": true, "unknown": true,
}

// NormalizeServiceState maps a systemd active state to the enum of the contract; anything
// else (a future systemd state, garbage) is "unknown".
func NormalizeServiceState(s string) string {
	if serviceStates[s] {
		return s
	}
	return "unknown"
}

// ServiceAckState builds the ack state from a watcher snapshot, ordered by appId so the
// frame is stable.
func ServiceAckState(snapshot map[string]string, observedAt time.Time) ServiceState {
	st := ServiceState{Statuses: make([]ServiceStatusEntry, 0, len(snapshot)), ObservedAt: observedAt.UTC()}
	for id, s := range snapshot {
		st.Statuses = append(st.Statuses, ServiceStatusEntry{AppID: id, State: NormalizeServiceState(s)})
	}
	sort.Slice(st.Statuses, func(i, j int) bool { return st.Statuses[i].AppID < st.Statuses[j].AppID })
	return st
}

// ServiceChanged is the event of a state change the watcher observed.
func ServiceChanged(appID, state string) Event {
	return ServiceStatus(appID, NormalizeServiceState(state))
}
