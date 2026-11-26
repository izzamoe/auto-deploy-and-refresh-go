// Package events is the protocol-3 event core of the admin panel (specs/002: plan.md
// section 3): topics and their permissions, client operations, the per-connection client
// state and the bus with seq/epoch and the resume ring buffer. It is transport-free (the
// WebSocket adaptor lives in internal/admin) and imports only the standard library, so
// internal/cancel, internal/store and the other producers never depend on it.
package events

import (
	"errors"
)

// Topic is a channel name of contracts/subscription.md section 1.
type Topic string

const (
	TopicSummary  Topic = "summary"
	TopicJobs     Topic = "jobs"
	TopicApps     Topic = "apps"
	TopicService  Topic = "service"
	TopicStats    Topic = "stats"
	TopicSettings Topic = "settings"
	TopicAccount  Topic = "account"
	TopicLogs     Topic = "logs:system"
)

// Principal kinds (the same strings as internal/auth).
const (
	PrincipalSession = "session"
	PrincipalToken   = "token"
)

// Principal is who opened the socket: a signed-in session or an API token.
type Principal struct {
	Kind      string
	UserID    string
	SessionID string
}

type topicInfo struct {
	token  bool // an API-token principal may subscribe (Q-6=A)
	stream bool // frames carry no seq and never enter the ring buffer
}

// registry is subscription.md section 1. Order is the order of Topics().
var registry = map[Topic]topicInfo{
	TopicSummary:  {token: true},
	TopicJobs:     {token: true},
	TopicApps:     {token: true},
	TopicService:  {token: true},
	TopicStats:    {},
	TopicSettings: {},
	TopicAccount:  {},
	TopicLogs:     {stream: true},
}

var topicOrder = []Topic{TopicSummary, TopicJobs, TopicApps, TopicService, TopicStats, TopicSettings, TopicAccount, TopicLogs}

// Topics lists every known topic in a fixed order.
func Topics() []Topic { return append([]Topic(nil), topicOrder...) }

// LookupTopic returns the topic with that name.
func LookupTopic(name string) (Topic, bool) {
	t := Topic(name)
	_, ok := registry[t]
	return t, ok
}

// Stream reports whether the topic's frames are a stream (no seq, no replay).
func (t Topic) Stream() bool { return registry[t].stream }

var (
	ErrUnknownTopic   = errors.New("unknown topic")
	ErrForbiddenTopic = errors.New("forbidden topic")
)

// Authorize is the permission matrix of subscription.md section 1 (Q-6=A): a session may
// subscribe to every topic; an API token only to summary, jobs, apps and service.
func Authorize(p Principal, t Topic) error {
	info, ok := registry[t]
	if !ok {
		return ErrUnknownTopic
	}
	switch p.Kind {
	case PrincipalSession:
		return nil
	case PrincipalToken:
		if info.token {
			return nil
		}
	}
	return ErrForbiddenTopic
}

// Visible reports whether an event of topic t that belongs to userID (account events
// only; empty for every other topic) may reach the principal: the topic is permitted and,
// for account, the event is the principal's own (FR-017).
func Visible(p Principal, t Topic, userID string) bool {
	if Authorize(p, t) != nil {
		return false
	}
	if t == TopicAccount {
		return userID != "" && userID == p.UserID
	}
	return true
}
