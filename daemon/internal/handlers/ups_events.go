package handlers

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"
)

// UPS events from upsmon (NUT). upsmon's NOTIFYCMD (ups.nix) writes one
// line per event to this socket:
//
//	ups_event:<NOTIFYTYPE>:<UPS name>:<NUT's message>
//
// Before, NOTIFYCMD was upssched with a no-op command: going on battery, low
// battery and a lost UPS connection alerted nobody. The socket is writable by
// the group upsmon runs its notify command as (nothing else is accepted on
// it: these lines can only raise alerts).

// upsEventLevel maps NUT notify types to alert levels ("" = not alerted).
var upsEventLevel = map[string]string{
	"ONLINE":   "info",     // power is back
	"ONBATT":   "warning",  // on battery
	"LOWBATT":  "critical", // low battery: shutdown follows
	"FSD":      "critical", // forced shutdown
	"SHUTDOWN": "critical", // the system shuts down now
	"COMMBAD":  "warning",  // connection to the UPS lost
	"COMMOK":   "info",     // connection back
	"NOCOMM":   "warning",  // UPS unreachable
	"REPLBATT": "warning",  // battery needs replacing
	"NOPARENT": "critical", // upsmon parent died: no shutdown possible
	"OVER":     "critical", // overload
	"OFF":      "warning",  // UPS output off
	"BYPASS":   "warning",  // on bypass: no battery protection
	"CAL":      "info",     // calibration
}

func upsEventMessage(kind, ups, nutMsg string) string {
	switch kind {
	case "ONBATT":
		return fmt.Sprintf("UPS %s is on battery (mains power lost). The system shuts down when the battery is low.", ups)
	case "ONLINE":
		return fmt.Sprintf("UPS %s is back on mains power.", ups)
	case "LOWBATT":
		return fmt.Sprintf("UPS %s battery is low: the system is shutting down.", ups)
	case "FSD", "SHUTDOWN":
		return fmt.Sprintf("The system is shutting down for UPS %s (%s).", ups, strings.TrimSpace(nutMsg))
	case "REPLBATT":
		return fmt.Sprintf("UPS %s reports that its battery needs to be replaced.", ups)
	case "COMMBAD", "NOCOMM":
		return fmt.Sprintf("No connection to UPS %s: a power failure would not shut the system down cleanly.", ups)
	}
	if m := strings.TrimSpace(nutMsg); m != "" {
		return m
	}
	return fmt.Sprintf("UPS %s: %s", ups, kind)
}

// handleUPSEventLine dispatches one line; it reports whether it was valid.
func handleUPSEventLine(line string) bool {
	parts := strings.SplitN(line, ":", 4)
	if len(parts) < 3 || parts[0] != "ups_event" {
		return false
	}
	kind, ups := strings.ToUpper(strings.TrimSpace(parts[1])), strings.TrimSpace(parts[2])
	level, ok := upsEventLevel[kind]
	if !ok || ups == "" || len(ups) > 64 {
		return false
	}
	msg := ""
	if len(parts) == 4 {
		msg = parts[3]
		if len(msg) > 300 {
			msg = msg[:300]
		}
	}
	log.Printf("UPS event: %s on %s", kind, ups)
	DispatchAlert(level, "ups."+strings.ToLower(kind), ups, upsEventMessage(kind, ups, msg))
	return true
}

// StartUPSEventListener listens on socketPath; group may write to it.
func StartUPSEventListener(ctx context.Context, socketPath, group string) {
	os.Remove(socketPath)
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Printf("UPS events: cannot listen on %s: %v", socketPath, err)
		return
	}
	mode := os.FileMode(0o600)
	if group != "" {
		if g, err := user.LookupGroup(group); err == nil {
			if gid, err := strconv.Atoi(g.Gid); err == nil && os.Chown(socketPath, 0, gid) == nil {
				mode = 0o660
			}
		} else {
			log.Printf("UPS events: group %s not found: only root can send events", group)
		}
	}
	os.Chmod(socketPath, mode) //nolint:errcheck
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		go func(c net.Conn) {
			defer c.Close()
			c.SetDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
			sc := bufio.NewScanner(c)
			for n := 0; sc.Scan() && n < 10; n++ {
				if !handleUPSEventLine(sc.Text()) {
					log.Printf("UPS events: ignored line %q", sc.Text())
				}
			}
		}(conn)
	}
}
