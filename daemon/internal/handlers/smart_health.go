package handlers

import (
	"encoding/json"
	"fmt"
)

// smartHealth evaluates the parts of `smartctl -H -A -j` that are not ATA
// attributes: the drive's overall verdict and the NVMe health log.
// hasHealth is false when the output has neither.
func smartHealth(out string) (risk string, reasons []string, hasHealth bool) {
	var r struct {
		SmartStatus *struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
		NVMe *struct {
			CriticalWarning int   `json:"critical_warning"`
			AvailableSpare  int   `json:"available_spare"`
			SpareThreshold  int   `json:"available_spare_threshold"`
			PercentageUsed  int   `json:"percentage_used"`
			MediaErrors     int64 `json:"media_errors"`
		} `json:"nvme_smart_health_information_log"`
	}
	if json.Unmarshal([]byte(out), &r) != nil {
		return "ok", nil, false
	}
	risk = "ok"
	raise := func(level, reason string) {
		if riskRank(level) > riskRank(risk) {
			risk = level
		}
		reasons = append(reasons, reason)
	}
	if r.SmartStatus != nil {
		hasHealth = true
		if !r.SmartStatus.Passed {
			raise("critical", "the drive reports its SMART health as FAILED")
		}
	}
	if n := r.NVMe; n != nil {
		hasHealth = true
		if n.CriticalWarning != 0 {
			raise("critical", fmt.Sprintf("NVMe critical warning 0x%02x (%s)", n.CriticalWarning, nvmeWarning(n.CriticalWarning)))
		}
		if n.SpareThreshold > 0 && n.AvailableSpare <= n.SpareThreshold {
			raise("critical", fmt.Sprintf("spare blocks at %d%% (threshold %d%%)", n.AvailableSpare, n.SpareThreshold))
		}
		if n.PercentageUsed >= 100 {
			raise("warning", fmt.Sprintf("%d%% of the rated endurance used", n.PercentageUsed))
		}
		if n.MediaErrors > 0 {
			raise("warning", fmt.Sprintf("%d media errors", n.MediaErrors))
		}
	}
	return risk, reasons, hasHealth
}

func nvmeWarning(bits int) string {
	names := []string{"spare below threshold", "temperature", "reliability degraded", "read-only", "volatile backup failed", "persistent memory read-only"}
	s := ""
	for i, n := range names {
		if bits&(1<<i) != 0 {
			if s != "" {
				s += ", "
			}
			s += n
		}
	}
	return s
}

func riskRank(r string) int {
	switch r {
	case "critical":
		return 2
	case "warning":
		return 1
	}
	return 0
}
