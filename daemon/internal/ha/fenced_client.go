package ha

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

const fencedSocket = "/run/dplaneos/fenced.sock"

// fencedRequest is the wire type for dplane-fenced socket commands.
type fencedRequest struct {
	Cmd    string `json:"cmd"`
	Device string `json:"device,omitempty"`
}

// fencedResponse is the wire type returned by dplane-fenced.
type fencedResponse struct {
	OK    bool        `json:"ok"`
	Error string      `json:"error,omitempty"`
	Data  interface{} `json:"data,omitempty"`
}

// fencedCall sends one command to dplane-fenced and returns the response.
// The connection is closed after each call (stateless protocol).
func fencedCall(req fencedRequest) (fencedResponse, error) {
	conn, err := net.DialTimeout("unix", fencedSocket, 3*time.Second)
	if err != nil {
		return fencedResponse{}, fmt.Errorf("connect to fenced: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fencedResponse{}, fmt.Errorf("send to fenced: %w", err)
	}

	var resp fencedResponse
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return fencedResponse{}, fmt.Errorf("read from fenced: %w", err)
	}
	return resp, nil
}

// FencedRelease signals dplane-fenced to release all SCSI-3 persistent
// reservations. Call this before exporting ZFS pools during graceful failover
// so the incoming primary can acquire the reservation without needing to
// preempt. If dplane-fenced is not running, the error is logged but the
// failover sequence continues (IPMI fencing provides the safety net).
func FencedRelease() error {
	resp, err := fencedCall(fencedRequest{Cmd: "RELEASE"})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("fenced RELEASE failed: %s", resp.Error)
	}
	return nil
}

// FencedStatus returns the current reservation state from dplane-fenced.
// Returns an error if the socket is unreachable (e.g. fenced not running).
func FencedStatus() (map[string]interface{}, error) {
	resp, err := fencedCall(fencedRequest{Cmd: "STATUS"})
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("fenced STATUS failed: %s", resp.Error)
	}
	if m, ok := resp.Data.(map[string]interface{}); ok {
		return m, nil
	}
	return nil, fmt.Errorf("unexpected STATUS response type")
}

// FencedPreempt asks dplane-fenced to fence a specific /dev/sgN device.
// Used during takeover when the incoming primary needs to explicitly fence a
// disk that was not automatically enumerated (e.g. a freshly connected shelf).
func FencedPreempt(device string) error {
	resp, err := fencedCall(fencedRequest{Cmd: "FENCE", Device: device})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("fenced FENCE %s failed: %s", device, resp.Error)
	}
	return nil
}
