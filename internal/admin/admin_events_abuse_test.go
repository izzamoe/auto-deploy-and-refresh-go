package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// T036 (FR-016, FR-026, NFR-008; events-ws.v3.md section 6): abuse of the protocol-3 op
// channel is refused with an error frame and does not disturb the socket, the third
// violation within 60 s closes it with 4429, and every refusal is audited without the frame.

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// captureSlog sends the default slog logger to a buffer for the test.
func captureSlog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

func (s *v3cSocket) sendRaw(t *testing.T, raw []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("send %d bytes: %v", len(raw), err)
	}
}

// expectOpen proves the socket still answers: a sub gets its ack.
func (s *v3cSocket) expectOpen(t *testing.T, ref string) {
	t.Helper()
	s.send(t, v3cSub(ref, nil, v3cTopic("summary")))
	s.await(t, "the socket still answers (subscribed{"+ref+"})", v3cAck("subscribed", ref))
}

func (s *v3cSocket) expectClosed(t *testing.T, want websocket.StatusCode) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case raw := <-s.frames:
			s.check(t, raw)
		case err := <-s.errc:
			if got := websocket.CloseStatus(err); got != want {
				t.Fatalf("close status = %d, want %d: %v", got, want, err)
			}
			return
		case <-deadline:
			t.Fatalf("the socket is still open after 3 s, want close %d", want)
		}
	}
}

// oversized is a 4097-byte op whose padding carries marker (must never reach a log).
func oversized(ref, marker string) []byte {
	head := fmt.Sprintf(`{"op":"sub","ref":%q,"topics":[{"name":"apps"}],"pad":"%s`, ref, marker)
	return []byte(head + strings.Repeat("x", 4097-len(head)-2) + `"}`)
}

func TestEventsV3_Abuse(t *testing.T) {
	sch := compileEventsV3Schema(t)
	env := func(t *testing.T) *wsSessionEnv {
		t.Helper()
		return newWSSessionEnv(t, "v3abuse_"+strings.NewReplacer("/", "_").Replace(t.Name()), time.Hour)
	}

	t.Run("frame_too_large", func(t *testing.T) {
		s := v3cDial(t, env(t), sch)
		raw := oversized("r1", "M4RK3R")
		if len(raw) != 4097 {
			t.Fatalf("test frame is %d bytes, want 4097", len(raw))
		}
		s.sendRaw(t, raw)
		er := s.await(t, "error{frame_too_large}", v3cIs("error"))
		if v3cPayload(er)["code"] != "frame_too_large" {
			t.Fatalf("4097-byte frame: %v, want error{frame_too_large}", er["_raw"])
		}
		// A frame of exactly 4096 bytes is accepted.
		head := `{"op":"sub","ref":"r2","topics":[{"name":"apps"}],"pad":"`
		s.sendRaw(t, []byte(head+strings.Repeat("y", 4096-len(head)-2)+`"}`))
		bad := s.await(t, "reply to a 4096-byte frame", func(m map[string]any) bool { return v3cPayload(m)["ref"] == "r2" })
		if bad["type"] != "error" || v3cPayload(bad)["code"] != "bad_op" { // "pad" is not an op field
			t.Fatalf("4096-byte frame: %v, want it parsed (bad_op for the unknown field)", bad["_raw"])
		}
		s.expectOpen(t, "r3")
	})

	t.Run("rate_limited", func(t *testing.T) {
		s := v3cDial(t, env(t), sch)
		for i := 1; i <= 21; i++ {
			s.send(t, v3cSub(fmt.Sprintf("r%d", i), nil, v3cTopic("summary")))
		}
		for i := 1; i <= 20; i++ {
			s.await(t, fmt.Sprintf("subscribed{r%d}", i), v3cAck("subscribed", fmt.Sprintf("r%d", i)))
		}
		er := s.await(t, "error{r21}", v3cAck("error", "r21"))
		if v3cPayload(er)["code"] != "rate_limited" {
			t.Fatalf("op 21 within 10 s: %v, want error{rate_limited}", er["_raw"])
		}
	})

	t.Run("too_many_topics", func(t *testing.T) {
		s := v3cDial(t, env(t), sch)
		topics := make([]map[string]any, 17)
		for i := range topics {
			topics[i] = v3cTopic("apps")
		}
		s.send(t, v3cSub("r1", nil, topics...))
		er := s.await(t, "error{r1}", v3cAck("error", "r1"))
		if v3cPayload(er)["code"] != "too_many_topics" {
			t.Fatalf("17 topics: %v, want error{too_many_topics}", er["_raw"])
		}
		s.expectOpen(t, "r2")
	})

	t.Run("invalid_json", func(t *testing.T) {
		s := v3cDial(t, env(t), sch)
		for i := 0; i < 5; i++ {
			s.sendRaw(t, []byte("{not json"))
			er := s.await(t, "error for invalid JSON", v3cIs("error"))
			if v3cPayload(er)["code"] != "bad_op" {
				t.Fatalf("invalid JSON: %v, want error{bad_op}", er["_raw"])
			}
		}
		s.expectOpen(t, "r1") // five invalid frames = one violation, not a close
	})

	t.Run("unknown_topic_is_not_abuse", func(t *testing.T) {
		s := v3cDial(t, env(t), sch)
		for i := 1; i <= 6; i++ {
			s.send(t, v3cSub(fmt.Sprintf("u%d", i), nil, v3cTopic("nope")))
			s.await(t, "error{unknown_topic}", v3cAck("error", fmt.Sprintf("u%d", i)))
		}
		s.expectOpen(t, "r1")
	})

	t.Run("close_4429", func(t *testing.T) {
		logs := captureSlog(t)
		s := v3cDial(t, env(t), sch)
		clientID := v3cPayload(s.hello)["clientId"].(string)
		for i := 1; i <= 3; i++ {
			s.sendRaw(t, oversized(fmt.Sprintf("r%d", i), "S3CR3TFRAME"))
			if i < 3 {
				s.await(t, "error{frame_too_large}", v3cIs("error"))
			}
		}
		s.expectClosed(t, 4429)
		out := logs.String()
		for _, want := range []string{"event=ws_policy", "reason=frame_too_large", "reason=closed_4429", "clientId=" + clientID} {
			if !strings.Contains(out, want) {
				t.Errorf("FR-026: audit log lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "S3CR3TFRAME") {
			t.Errorf("FR-026: the audit log carries frame content:\n%s", out)
		}
		var lines []map[string]any
		for _, m := range s.seen {
			if m["type"] == "error" {
				lines = append(lines, m)
			}
		}
		if len(lines) != 3 {
			raws, _ := json.Marshal(lines)
			t.Errorf("want one error frame per violation before the close, got %d: %s", len(lines), raws)
		}
	})
}
