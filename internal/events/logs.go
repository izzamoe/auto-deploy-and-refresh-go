package events

import (
	"time"
)

// LogLine is one line of topic logs:system (events-ws.v3.md 2.2): a journal entry
// rendered as text, at most 4 KiB.
type LogLine struct {
	TS       time.Time `json:"ts"`
	Priority int       `json:"priority"`
	Message  string    `json:"message"`
}

// LogsLinesPayload is the payload of logs.lines: a batch (at most 200 lines) and the
// journal cursor of its last line (the `after` of a later sub).
type LogsLinesPayload struct {
	Lines  []LogLine `json:"lines"`
	Cursor string    `json:"cursor"`
}

// LogsGapPayload is the payload of logs.gap: lines lost to the rate limit, or a cursor
// older than what the shared follower kept (reload the REST snapshot).
type LogsGapPayload struct {
	Dropped int    `json:"dropped"`
	Reason  string `json:"reason"`
}

// MaxLogBatch is the largest number of lines of one logs.lines frame.
const MaxLogBatch = 200

// Gap reasons of logs.gap.
const (
	GapRate         = "rate"
	GapCursorBehind = "cursor_behind"
)

// LogsLines is a stream event of topic logs:system (no seq, never replayed from the ring).
func LogsLines(lines []LogLine, cursor string) Event {
	if lines == nil {
		lines = []LogLine{}
	}
	return Event{Topic: TopicLogs, Type: TypeLogsLines, Payload: LogsLinesPayload{Lines: lines, Cursor: cursor}}
}

// LogsGap is the stream event of a gap in topic logs:system.
func LogsGap(dropped int, reason string) Event {
	return Event{Topic: TopicLogs, Type: TypeLogsGap, Payload: LogsGapPayload{Dropped: dropped, Reason: reason}}
}

// LogsJoin is what a socket that subscribes to logs:system gets besides the live stream
// (subscription.md section 5): the recent lines after its cursor, in batches, or Behind
// when its cursor is older than what the shared follower kept (logs.gap{cursor_behind}:
// the client reloads the REST snapshot; no second process is started).
type LogsJoin struct {
	Batches []LogsLinesPayload
	Behind  bool
}

// JournalFollower is what topic logs:system needs from the shared journal follower
// (internal/deploy.JournalFollower): a reference per subscribed socket with the cursor the
// socket already has, and a stop at shutdown.
type JournalFollower interface {
	Acquire(after string) LogsJoin
	Release()
	Close()
}
