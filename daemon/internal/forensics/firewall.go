package forensics

import (
	"encoding/json"
	"fmt"
	"sort"

	"dplaned/internal/cmdutil"
)

// NftablesOutput represents the JSON structure from nft -j list ruleset
type NftablesOutput struct {
	Nftables []struct {
		Rule *NftRule `json:"rule,omitempty"`
	} `json:"nftables"`
}

type NftRule struct {
	Family string    `json:"family"`
	Table  string    `json:"table"`
	Chain  string    `json:"chain"`
	Expr   []NftExpr `json:"expr"`
}

type NftExpr map[string]interface{}

type NftMatch struct {
	Left  NftOp   `json:"left"`
	Op    string  `json:"op"`
	Right interface{} `json:"right"` // Can be float64 or []interface{} (port or port list)
}

type NftOp struct {
	Payload *NftPayload `json:"payload,omitempty"`
}

type NftPayload struct {
	Protocol string `json:"protocol"`
	Field    string `json:"field"`
}

// GetPhysicalFirewallPorts probes the kernel via nftables and returns all 'accept' ports.
// It specifically looks for rules that match 'dport' and have an 'accept' expression.
func GetPhysicalFirewallPorts() (tcp []int, udp []int, err error) {
	// Execute the whitelisted forensic probe
	out, err := cmdutil.RunFast("nft_list_json")
	if err != nil {
		return nil, nil, fmt.Errorf("forensics: nft probe failed: %w", err)
	}

	return parseNftJSON(out)
}

func parseNftJSON(data []byte) (tcp []int, udp []int, err error) {
	var nft NftablesOutput
	if err := json.Unmarshal(data, &nft); err != nil {
		return nil, nil, fmt.Errorf("forensics: decode nft output: %w", err)
	}

	tcpMap := make(map[int]bool)
	udpMap := make(map[int]bool)

	for _, item := range nft.Nftables {
		if item.Rule == nil {
			continue
		}

		// A rule is "acceptive" only if it contains an 'accept' expression.
		isAccept := false
		for _, expr := range item.Rule.Expr {
			if _, ok := expr["accept"]; ok {
				isAccept = true
				break
			}
		}

		if !isAccept {
			continue
		}

		// Now extract the ports from match expressions in this rule.
		for _, expr := range item.Rule.Expr {
			rawMatch, ok := expr["match"]
			if !ok {
				continue
			}

			// Manually decode the match portion
			matchData, err := json.Marshal(rawMatch)
			if err != nil {
				continue
			}
			var m NftMatch
			if err := json.Unmarshal(matchData, &m); err != nil {
				continue
			}

			if m.Left.Payload == nil || m.Left.Payload.Field != "dport" {
				continue
			}

			proto := m.Left.Payload.Protocol
			targetMap := tcpMap
			if proto == "udp" {
				targetMap = udpMap
			}

			// Handle single port (JSON number -> float64) or port set ([]interface{})
			switch v := m.Right.(type) {
			case float64:
				targetMap[int(v)] = true
			case []interface{}:
				for _, p := range v {
					if port, ok := p.(float64); ok {
						targetMap[int(port)] = true
					}
				}
			}
		}
	}

	for p := range tcpMap {
		tcp = append(tcp, p)
	}
	for p := range udpMap {
		udp = append(udp, p)
	}

	sort.Ints(tcp)
	sort.Ints(udp)

	return tcp, udp, nil
}
