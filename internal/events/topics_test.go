package events

import (
	"errors"
	"testing"
)

// T035 (FR-017, FR-033, NFR-008; subscription.md section 1, Q-6=A in answers.md): the
// permission matrix per principal, the account filter, unknown topics.

func TestTopics_AuthzMatrix(t *testing.T) {
	sessionP := Principal{Kind: PrincipalSession, UserID: "u1", SessionID: "s1"}
	tokenP := Principal{Kind: PrincipalToken, UserID: "u1"}
	// Q-6=A: an API token may subscribe to summary, jobs, apps and service only.
	want := map[Topic][2]bool{ // {session, token}
		TopicSummary:  {true, true},
		TopicJobs:     {true, true},
		TopicApps:     {true, true},
		TopicService:  {true, true},
		TopicStats:    {true, false},
		TopicSettings: {true, false},
		TopicAccount:  {true, false},
		TopicLogs:     {true, false},
	}
	if len(want) != 8 || len(Topics()) != 8 {
		t.Fatalf("the registry has %d topics, the matrix %d; want 8 (subscription.md section 1)", len(Topics()), len(want))
	}
	cases := 0
	for _, topic := range Topics() {
		exp, ok := want[topic]
		if !ok {
			t.Fatalf("topic %q is not in the matrix", topic)
		}
		for i, p := range []Principal{sessionP, tokenP} {
			cases++
			err := Authorize(p, topic)
			if exp[i] && err != nil {
				t.Errorf("%s principal, topic %s: refused (%v), want allowed", p.Kind, topic, err)
			}
			if !exp[i] && !errors.Is(err, ErrForbiddenTopic) {
				t.Errorf("%s principal, topic %s: err = %v, want ErrForbiddenTopic", p.Kind, topic, err)
			}
		}
	}
	if cases != 16 {
		t.Fatalf("checked %d cases, want 2x8", cases)
	}
	// A principal of no known kind (e.g. no auth context) gets nothing.
	for _, topic := range Topics() {
		if Authorize(Principal{}, topic) == nil {
			t.Errorf("an unknown principal may subscribe to %s", topic)
		}
	}
	// The bus applies the same matrix to a token's sub.
	b := NewBus()
	c := NewClient(tokenP)
	b.Register(c)
	res := b.Subscribe(c, sub(Topics()...), nil)
	if len(res.Accepted) != 4 || len(res.Refused) != 4 {
		t.Fatalf("token sub of all topics: accepted %d refused %v, want 4 and [stats settings account logs:system]", len(res.Accepted), res.Refused)
	}
}

func TestTopics_AccountOnlySameUser(t *testing.T) {
	u1 := Principal{Kind: PrincipalSession, UserID: "u1", SessionID: "s1"}
	if !Visible(u1, TopicAccount, "u1") {
		t.Error("u1 must see its own account events")
	}
	if Visible(u1, TopicAccount, "u2") {
		t.Error("FR-017: u1 sees an account event of u2")
	}
	if Visible(u1, TopicAccount, "") {
		t.Error("an account event without a user reaches a session")
	}
	if Visible(Principal{Kind: PrincipalToken, UserID: "u1"}, TopicAccount, "u1") {
		t.Error("an API token sees account events")
	}
	if !Visible(u1, TopicApps, "") {
		t.Error("a non-account topic is filtered by user")
	}

	// End to end through the bus: two sessions of different users, both on account.
	b := NewBus()
	c1, c2 := NewClient(u1), NewClient(Principal{Kind: PrincipalSession, UserID: "u2", SessionID: "s2"})
	for _, c := range []*Client{c1, c2} {
		b.Register(c)
		b.Subscribe(c, sub(TopicAccount), nil)
	}
	_, _ = b.Publish(Event{Topic: TopicAccount, Type: "account.changed", UserID: "u1", Payload: map[string]string{"section": "tokens"}})
	if n1, n2 := len(drainQueue(c1)), len(drainQueue(c2)); n1 != 1 || n2 != 0 {
		t.Fatalf("account event of u1 reached u1 %d times and u2 %d times, want 1 and 0", n1, n2)
	}
}

func TestTopics_UnknownRejected(t *testing.T) {
	for _, name := range []string{"", "nope", "logs", "logs:app", "Apps", "jobs ", "summary\x00"} {
		if _, ok := LookupTopic(name); ok {
			t.Errorf("LookupTopic(%q) accepted an unknown name", name)
		}
		if err := Authorize(Principal{Kind: PrincipalSession, UserID: "u1"}, Topic(name)); !errors.Is(err, ErrUnknownTopic) {
			t.Errorf("Authorize(%q) = %v, want ErrUnknownTopic", name, err)
		}
	}
	op, perr := ParseOp([]byte(`{"op":"sub","ref":"r1","topics":[{"name":"apps"},{"name":"nope"}]}`))
	if perr != nil {
		t.Fatalf("a sub with one unknown topic must still apply the known one: %v", perr)
	}
	if len(op.Subs) != 1 || op.Subs[0].Topic != TopicApps || len(op.Errors) != 1 || op.Errors[0].Code != CodeUnknownTopic || op.Errors[0].Topic != "nope" || op.Errors[0].Ref != "r1" {
		t.Fatalf("op = %+v, want apps subscribed and error{unknown_topic, nope, r1}", op)
	}
	op, perr = ParseOp([]byte(`{"op":"unsub","ref":"r2","topics":["nope"]}`))
	if perr != nil || len(op.Errors) != 1 || op.Errors[0].Code != CodeUnknownTopic {
		t.Fatalf("unsub of an unknown topic: op %+v err %v", op, perr)
	}
	if _, err := (&Bus{}).Publish(Event{Topic: "nope"}); !errors.Is(err, ErrUnknownTopic) {
		t.Fatalf("Publish to an unknown topic = %v, want ErrUnknownTopic", err)
	}
}
