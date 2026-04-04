package zfs

import (
	"strconv"
	"strings"
	"time"
)

// SendProgressState tracks zfs send -P stderr stream for rate/ETA updates.
type SendProgressState struct {
	TotalSize int64
	LastSent  int64
	LastTime  time.Time
}

// FeedSendProgressLine parses one line of `zfs send -P` stderr. When emit is true,
// update contains percent, bytes_sent, total_bytes, rate_bps, rate_mbs, eta_seconds
// suitable for jobs.Job.Progress and WebSocket payloads.
func FeedSendProgressLine(line string, st *SendProgressState, minInterval time.Duration) (update map[string]interface{}, emit bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return nil, false
	}
	switch fields[0] {
	case "size":
		st.TotalSize, _ = strconv.ParseInt(fields[1], 10, 64)
	case "sent":
		sent, _ := strconv.ParseInt(fields[1], 10, 64)
		now := time.Now()
		if st.LastTime.IsZero() {
			st.LastTime = now
			st.LastSent = sent
			return nil, false
		}
		elapsed := now.Sub(st.LastTime).Seconds()
		if elapsed < minInterval.Seconds() {
			return nil, false
		}
		rate := float64(sent-st.LastSent) / elapsed
		var percent float64
		if st.TotalSize > 0 {
			percent = float64(sent) / float64(st.TotalSize) * 100
		}
		eta := int64(-1)
		if rate > 0 && st.TotalSize > 0 {
			eta = int64(float64(st.TotalSize-sent) / rate)
		}
		st.LastSent = sent
		st.LastTime = now
		return map[string]interface{}{
			"percent":     percent,
			"bytes_sent":  sent,
			"total_bytes": st.TotalSize,
			"rate_bps":    rate,
			"rate_mbs":    rate / (1024 * 1024),
			"eta_seconds": eta,
		}, true
	}
	return nil, false
}
