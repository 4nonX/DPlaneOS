package zfs

import (
	"testing"
	"time"
)

func TestFeedSendProgressLine(t *testing.T) {
	var st SendProgressState
	if _, emit := FeedSendProgressLine("size 1000", &st, 500*time.Millisecond); emit {
		t.Fatal("size should not emit")
	}
	if st.TotalSize != 1000 {
		t.Fatalf("total %d", st.TotalSize)
	}
	up, emit := FeedSendProgressLine("sent 100", &st, 500*time.Millisecond)
	if emit || up != nil {
		t.Fatalf("first sent should not emit (interval)")
	}
	st.LastTime = st.LastTime.Add(-600 * time.Millisecond)
	up, emit = FeedSendProgressLine("sent 200", &st, 500*time.Millisecond)
	if !emit || up == nil {
		t.Fatal("expected emit")
	}
	if up["bytes_sent"].(int64) != 200 {
		t.Fatalf("%v", up["bytes_sent"])
	}
}
