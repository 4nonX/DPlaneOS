package gitops

import (
	"fmt"
	"net"
	"time"
)

// ServiceStatus represents the functional health of a specific protocol.
type ServiceStatus struct {
	Name      string `json:"name"`
	Port      int    `json:"port"`
	IsHealthy bool   `json:"is_healthy"`
	Error     string `json:"error,omitempty"`
}

// VerificationReport aggregates the results of the synthetic probes.
type VerificationReport struct {
	Timestamp      time.Time       `json:"timestamp"`
	AllFunctional  bool            `json:"all_functional"`
	ServiceResults []ServiceStatus `json:"service_results"`
}

// VerifyAppliedServices performs synthetic TCP liveness probes against the desired state.
// It uses a retry-with-backoff strategy to account for service startup lag.
func VerifyAppliedServices(desired *DesiredState) *VerificationReport {
	report := &VerificationReport{
		Timestamp:     time.Now(),
		AllFunctional: true,
	}

	// 1. Define probes based on the Intent Plane (DesiredState)
	var probes []struct {
		name string
		port int
	}

	// If any SMB shares are defined, expect port 445 to be open
	if len(desired.Shares) > 0 {
		probes = append(probes, struct{ name string; port int }{"SMB", 445})
	}
	// If any NFS exports are defined, expect port 2049 to be open
	if len(desired.NFS) > 0 {
		probes = append(probes, struct{ name string; port int }{"NFS", 2049})
	}
	// Always probe essential management services (Phase 12.6)
	probes = append(probes, struct{ name string; port int }{"API", 9000})
	probes = append(probes, struct{ name string; port int }{"SSH", 22})

	// 2. Execute probes with linear backoff (3 attempts over ~10 seconds)
	for _, p := range probes {
		status := ServiceStatus{Name: p.name, Port: p.port, IsHealthy: true}

		err := probeWithRetry("localhost", p.port, 3, 2*time.Second)
		if err != nil {
			status.IsHealthy = false
			status.Error = fmt.Sprintf("Service failed to respond on port %d after retries: %v", p.port, err)
			report.AllFunctional = false
		}

		report.ServiceResults = append(report.ServiceResults, status)
	}

	return report
}

// probeWithRetry attempts a TCP dial with a simple linear backoff.
func probeWithRetry(host string, port int, maxRetries int, baseDelay time.Duration) error {
	address := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		// Linear backoff: 0s, 2s, 4s...
		if i > 0 {
			time.Sleep(baseDelay * time.Duration(i))
		}

		conn, err := net.DialTimeout("tcp", address, 2*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		lastErr = err
	}

	return lastErr
}
