package events

import (
	"bytes"
	"encoding/json"
	"regexp"
	"time"
)

// Limits announced in hello.limits (events-ws.v3.md 2.1, subscription.md section 3).
const (
	MaxTopics     = 16
	MaxOpsPer10s  = 20
	MaxFrameBytes = 4096
)

// ErrorCode is the payload.code of an error frame (events-ws.v3.md 2.1).
type ErrorCode string

const (
	CodeUnknownTopic   ErrorCode = "unknown_topic"
	CodeForbiddenTopic ErrorCode = "forbidden_topic"
	CodeBadOp          ErrorCode = "bad_op"
	CodeTooManyTopics  ErrorCode = "too_many_topics"
	CodeRateLimited    ErrorCode = "rate_limited"
	CodeFrameTooLarge  ErrorCode = "frame_too_large"
)

// OpError is one error frame: the op (or one topic of it) was refused.
type OpError struct {
	Ref   string
	Code  ErrorCode
	Topic string
	// InvalidJSON marks a frame that was not a JSON object at all (counted separately, T036).
	InvalidJSON bool
}

func (e *OpError) Error() string {
	if e.Topic != "" {
		return string(e.Code) + ": " + e.Topic
	}
	return string(e.Code)
}

// TopicOpts are the options of one subscription (data-model.md "Topic Opts").
type TopicOpts struct {
	Progress bool   // jobs: receive compact progress frames (default true)
	After    string // logs:system: journal cursor
	TZ       string // stats: IANA zone of the StatsClock (default UTC)
}

// TopicSub is one entry of a sub op.
type TopicSub struct {
	Topic Topic
	Opts  TopicOpts
}

// Resume is the {epoch, seq} of a sub (events-ws.v3.md section 5).
type Resume struct {
	Epoch string
	Seq   uint64
}

// Op is a parsed client operation (events-ws.v3.md section 4).
type Op struct {
	Kind   string // "sub" or "unsub"
	Ref    string
	Subs   []TopicSub
	Unsubs []Topic
	Resume *Resume
	// Errors are refused topics (unknown names); the other topics of the op still apply.
	Errors []OpError
}

var refPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,16}$`)

const (
	maxAfterLen    = 512
	maxTZLen       = 64
	maxSafeInteger = 1<<53 - 1 // largest seq a JavaScript client can send exactly
)

// ParseOp validates one client frame. A frame that cannot be applied at all returns an
// OpError (bad_op); unknown topic names are reported per topic in Op.Errors.
func ParseOp(data []byte) (Op, *OpError) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return Op{}, &OpError{Code: CodeBadOp, InvalidJSON: true}
	}
	var op Op
	// ref first, so later errors can be correlated.
	if r, ok := raw["ref"]; ok {
		var ref string
		if json.Unmarshal(r, &ref) != nil || !refPattern.MatchString(ref) {
			return Op{}, &OpError{Code: CodeBadOp}
		}
		op.Ref = ref
	} else {
		return Op{}, &OpError{Code: CodeBadOp}
	}
	bad := &OpError{Ref: op.Ref, Code: CodeBadOp}
	if json.Unmarshal(raw["op"], &op.Kind) != nil || (op.Kind != "sub" && op.Kind != "unsub") {
		return Op{}, bad
	}
	for k := range raw {
		switch k {
		case "op", "ref", "topics":
		case "resume":
			if op.Kind != "sub" {
				return Op{}, bad
			}
		default:
			return Op{}, bad
		}
	}
	topics, ok := raw["topics"]
	if !ok {
		return Op{}, bad
	}
	var items []json.RawMessage
	if json.Unmarshal(topics, &items) != nil || len(items) == 0 {
		return Op{}, bad
	}
	if op.Kind == "unsub" {
		for _, it := range items {
			var name string
			if json.Unmarshal(it, &name) != nil {
				return Op{}, bad
			}
			t, known := LookupTopic(name)
			if !known {
				op.Errors = append(op.Errors, OpError{Ref: op.Ref, Code: CodeUnknownTopic, Topic: clip(name)})
				continue
			}
			op.Unsubs = append(op.Unsubs, t)
		}
		return op, nil
	}
	for _, it := range items {
		sub, perr, ok := parseTopicSub(it, op.Ref)
		if !ok {
			return Op{}, bad
		}
		if perr != nil {
			op.Errors = append(op.Errors, *perr)
			continue
		}
		op.Subs = append(op.Subs, sub)
	}
	if r, ok := raw["resume"]; ok {
		var res struct {
			Epoch *string  `json:"epoch"`
			Seq   *float64 `json:"seq"`
		}
		dec := json.NewDecoder(bytes.NewReader(r))
		dec.DisallowUnknownFields()
		if dec.Decode(&res) != nil || res.Epoch == nil || res.Seq == nil || *res.Seq < 0 || *res.Seq > maxSafeInteger || *res.Seq != float64(uint64(*res.Seq)) || len(*res.Epoch) > 64 {
			return Op{}, bad
		}
		op.Resume = &Resume{Epoch: *res.Epoch, Seq: uint64(*res.Seq)}
	}
	return op, nil
}

// parseTopicSub decodes {name, progress?, after?, tz?}. ok=false is a bad_op for the whole
// frame; perr is a per-topic refusal (unknown name).
func parseTopicSub(it json.RawMessage, ref string) (TopicSub, *OpError, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(it, &obj) != nil || obj == nil {
		return TopicSub{}, nil, false
	}
	var name string
	if json.Unmarshal(obj["name"], &name) != nil || name == "" {
		return TopicSub{}, nil, false
	}
	t, known := LookupTopic(name)
	if !known {
		return TopicSub{}, &OpError{Ref: ref, Code: CodeUnknownTopic, Topic: clip(name)}, true
	}
	sub := TopicSub{Topic: t, Opts: TopicOpts{Progress: t == TopicJobs, TZ: "UTC"}}
	for k, v := range obj {
		switch {
		case k == "name":
		case k == "progress" && t == TopicJobs:
			if json.Unmarshal(v, &sub.Opts.Progress) != nil {
				return TopicSub{}, nil, false
			}
		case k == "after" && t == TopicLogs:
			if json.Unmarshal(v, &sub.Opts.After) != nil || len(sub.Opts.After) > maxAfterLen {
				return TopicSub{}, nil, false
			}
		case k == "tz" && t == TopicStats:
			if json.Unmarshal(v, &sub.Opts.TZ) != nil || sub.Opts.TZ == "" || len(sub.Opts.TZ) > maxTZLen {
				return TopicSub{}, nil, false
			}
			if _, err := time.LoadLocation(sub.Opts.TZ); err != nil {
				return TopicSub{}, nil, false
			}
		default:
			return TopicSub{}, nil, false
		}
	}
	return sub, nil, true
}

// clip bounds a client-supplied topic name echoed in an error frame.
func clip(s string) string {
	if len(s) > 64 {
		return s[:64]
	}
	return s
}
