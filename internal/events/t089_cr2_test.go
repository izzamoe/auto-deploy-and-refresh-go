package events

import (
	"strings"
	"testing"
)

// T089 CR-2: the `after` of logs:system must look like a journald cursor
// (s=…;i=…;b=…;m=…;t=…;x=…, lowercase hex values). Anything else is refused with bad_op
// before it can reach the shared journal follower.
func TestT089_CR2_ParseOpValidatesJournalCursor(t *testing.T) {
	for _, bad := range []string{
		"garbage-cursor", "s=1;i=2;--lines=all", "s=XYZ", "s=", "=1", "s=1;;i=2", "s=1 i=2", "s=1;q=2",
		"s=" + strings.Repeat("a", 600),
	} {
		raw := `{"op":"sub","ref":"r1","topics":[{"name":"logs:system","after":"` + bad + `"}]}`
		if _, perr := ParseOp([]byte(raw)); perr == nil || perr.Code != CodeBadOp || perr.Ref != "r1" {
			t.Errorf("CR-2: after=%q: perr = %+v, want bad_op", bad, perr)
		}
	}
	for _, good := range []string{
		"s=1", "s=1;i=2",
		"s=0b1e2c3d4f5a6b7c8d9e0f1a2b3c4d5e;i=1a2b3;b=7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b2a;m=1f2e3d;t=5f6e7d8c9b0a1;x=abcdef0123456789",
	} {
		raw := `{"op":"sub","ref":"r1","topics":[{"name":"logs:system","after":"` + good + `"}]}`
		op, perr := ParseOp([]byte(raw))
		if perr != nil || len(op.Subs) != 1 || op.Subs[0].Opts.After != good {
			t.Errorf("after=%q: op=%+v perr=%+v, want it accepted", good, op, perr)
		}
	}
}
