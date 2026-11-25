package deploy

import (
	"bytes"
	"strings"
	"testing"
)

// FR-050/FR-058: a journalctl read keeps only the tail, so memory stays bounded.
func TestTailBuffer_KeepsOnlyTheLastBytes(t *testing.T) {
	b := &tailBuffer{max: 1000}
	var all bytes.Buffer
	for i := 0; i < 500; i++ {
		chunk := strings.Repeat(string(rune('a'+i%26)), 37) + "\n"
		all.WriteString(chunk)
		if n, err := b.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write = %d, %v", n, err)
		}
		if len(b.buf) > 2*b.max+len(chunk) {
			t.Fatalf("the buffer grew to %d bytes with max %d", len(b.buf), b.max)
		}
	}
	got := b.Bytes()
	if len(got) > 1000 || !bytes.HasSuffix(all.Bytes(), got) || len(got) < 900 {
		t.Errorf("tail = %d bytes, want the last <= 1000 bytes of the stream", len(got))
	}
}
